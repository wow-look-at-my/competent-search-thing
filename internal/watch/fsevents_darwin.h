//go:build darwin

/*
 * C declarations shared between fsevents_darwin.c and the cgo
 * preamble of fsevents_darwin.go: the FSEvents stream lifecycle
 * helpers behind the darwin whole-filesystem watch backend. See
 * fsevents_darwin.c for the mechanism notes.
 */

#ifndef CS_FSEVENTS_DARWIN_H
#define CS_FSEVENTS_DARWIN_H

#include <stdint.h>

#include <CoreServices/CoreServices.h>
#include <dispatch/dispatch.h>

/* A private serial dispatch queue for one stream's callbacks; NULL on
 * failure. Release with csFSEQueueRelease after csFSEStop. */
dispatch_queue_t csFSEQueueCreate(void);

/* Synchronously drain the queue: returns once every callback enqueued
 * before the call has finished. Called after csFSEStop so the Go side
 * can safely delete the cgo.Handle the callbacks dereference. */
void csFSEQueueDrain(dispatch_queue_t q);

void csFSEQueueRelease(dispatch_queue_t q);

/* Create and start one FSEvents stream over the n paths, delivering
 * batches to the exported Go trampoline keyed by handle (a
 * cgo.Handle) on the given queue. sinceNow, FileEvents|NoDefer,
 * latency in seconds. Returns NULL when the stream cannot be created
 * or started. */
FSEventStreamRef csFSEStart(uintptr_t handle, char **paths, int n,
    double latency, dispatch_queue_t q);

/* Stop, invalidate, and release a stream returned by csFSEStart. Per
 * the FSEvents contract Invalidate must follow Stop; after it returns
 * no NEW callback is enqueued (in-flight ones are drained via
 * csFSEQueueDrain). */
void csFSEStop(FSEventStreamRef stream);

#endif /* CS_FSEVENTS_DARWIN_H */
