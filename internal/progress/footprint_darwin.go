//go:build darwin

package progress

// The package's ONE cgo file (the internal/sysstats readers_darwin.go
// pattern): mach task_info has no pure-Go spelling. Links against
// libSystem only -- no extra frameworks, no LDFLAGS. Like sysstats,
// this makes darwin builds of the package require cgo, which every
// real darwin build of the app already does (Wails). Real-call
// coverage lives in rss_darwin_test.go on the mac CI job.

/*
#include <mach/mach.h>

// cs_phys_footprint returns the calling task's CURRENT physical
// memory footprint in bytes -- TASK_VM_INFO's phys_footprint, the
// figure Activity Monitor's "Memory" column shows -- or 0 when the
// kernel cannot report it. phys_footprint was added in the rev1
// task_vm_info layout, so a count below TASK_VM_INFO_REV1_COUNT
// means the field was not filled.
static unsigned long long
cs_phys_footprint(void)
{
	task_vm_info_data_t info;
	mach_msg_type_number_t count = TASK_VM_INFO_COUNT;
	kern_return_t kr = task_info(mach_task_self(), TASK_VM_INFO, (task_info_t)&info, &count);
	if (kr != KERN_SUCCESS || count < TASK_VM_INFO_REV1_COUNT) {
		return 0;
	}
	return (unsigned long long)info.phys_footprint;
}
*/
import "C"

// physFootprintBytes returns the process's current physical memory
// footprint in bytes, 0 when unavailable (rssBytes then falls back to
// the getrusage peak).
func physFootprintBytes() uint64 {
	return uint64(C.cs_phys_footprint())
}
