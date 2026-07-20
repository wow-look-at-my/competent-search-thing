//go:build darwin

/*
 * FSEvents stream glue for the darwin whole-filesystem watch backend.
 *
 * API usage provenance (CoreServices FSEvents.h, all available since
 * macOS 10.6/10.7 and unchanged since):
 *   - FSEventStreamCreate(alloc, callback, context, pathsToWatch,
 *     sinceWhen, latency, flags) with
 *     kFSEventStreamEventIdSinceNow (no history replay; the watch
 *     layer starts only after the initial walk) and
 *     kFSEventStreamCreateFlagFileEvents|kFSEventStreamCreateFlagNoDefer
 *     (per-file paths + Item* flags so intake can drop pure content
 *     churn, exactly like the watcher drops Write/Chmod; NoDefer
 *     delivers the first event of a burst immediately).
 *   - FSEventStreamSetDispatchQueue (10.6+) instead of the run-loop
 *     scheduling deprecated since macOS 13: callbacks arrive on a
 *     private serial queue, no run loop to own or pump.
 *   - Teardown per the header docs: FSEventStreamStop ->
 *     FSEventStreamInvalidate -> FSEventStreamRelease, then the queue
 *     is drained (dispatch_sync_f of a no-op, so any in-flight
 *     callback has returned) and released.
 *
 * The callback forwards each batch to the exported Go trampoline
 * (goFSEventsCallback in fsevents_darwin.go) keyed by a cgo.Handle
 * stashed in context.info -- the launchmint_linux.c precedent. The
 * event ids are dropped: the watcher's event model is dirty paths
 * only, and id-wrap is signaled through the per-record flags.
 */

#include "fsevents_darwin.h"
#include "_cgo_export.h"

static void
csFSECallback(ConstFSEventStreamRef streamRef, void *clientCallBackInfo,
    size_t numEvents, void *eventPaths,
    const FSEventStreamEventFlags eventFlags[],
    const FSEventStreamEventId eventIds[])
{
	(void)streamRef;
	(void)eventIds;
	/* With FileEvents (and no UseCFTypes) eventPaths is char**.
	 * FSEventStreamEventFlags is UInt32 == unsigned int on darwin;
	 * the cast only drops const for the exported prototype. */
	goFSEventsCallback((uintptr_t)clientCallBackInfo, numEvents,
	    (char **)eventPaths, (unsigned int *)eventFlags);
}

dispatch_queue_t
csFSEQueueCreate(void)
{
	return dispatch_queue_create(
	    "competent-search-thing.fsevents", DISPATCH_QUEUE_SERIAL);
}

static void
csFSENoop(void *ctx)
{
	(void)ctx;
}

void
csFSEQueueDrain(dispatch_queue_t q)
{
	/* dispatch_sync_f, not a block: plain C, no blocks runtime
	 * dependency. On a serial queue this returns only after every
	 * previously enqueued callback has finished. */
	dispatch_sync_f(q, NULL, csFSENoop);
}

void
csFSEQueueRelease(dispatch_queue_t q)
{
	dispatch_release(q);
}

FSEventStreamRef
csFSEStart(uintptr_t handle, char **paths, int n, double latency,
    dispatch_queue_t q)
{
	CFMutableArrayRef arr;
	FSEventStreamContext ctx;
	FSEventStreamRef stream;
	int i;

	arr = CFArrayCreateMutable(NULL, n, &kCFTypeArrayCallBacks);
	if (arr == NULL) {
		return NULL;
	}
	for (i = 0; i < n; i++) {
		CFStringRef s = CFStringCreateWithCString(NULL, paths[i],
		    kCFStringEncodingUTF8);
		if (s == NULL) {
			CFRelease(arr);
			return NULL;
		}
		CFArrayAppendValue(arr, s);
		CFRelease(s); /* the array retains it */
	}
	ctx.version = 0;
	ctx.info = (void *)handle;
	ctx.retain = NULL;
	ctx.release = NULL;
	ctx.copyDescription = NULL;
	stream = FSEventStreamCreate(NULL, csFSECallback, &ctx, arr,
	    kFSEventStreamEventIdSinceNow, (CFTimeInterval)latency,
	    kFSEventStreamCreateFlagFileEvents |
	    kFSEventStreamCreateFlagNoDefer);
	CFRelease(arr); /* FSEventStreamCreate copies the paths */
	if (stream == NULL) {
		return NULL;
	}
	FSEventStreamSetDispatchQueue(stream, q);
	if (!FSEventStreamStart(stream)) {
		FSEventStreamInvalidate(stream);
		FSEventStreamRelease(stream);
		return NULL;
	}
	return stream;
}

void
csFSEStop(FSEventStreamRef stream)
{
	FSEventStreamStop(stream);
	FSEventStreamInvalidate(stream);
	FSEventStreamRelease(stream);
}
