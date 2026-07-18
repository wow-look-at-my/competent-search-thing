//go:build linux

/*
 * C declarations shared between launchmint_linux.c and the cgo
 * preamble of launchmint_linux.go: the GTK-main-thread dispatch
 * helpers and the launch-credential mint. See launchmint_linux.c for
 * the mechanism notes.
 */

#ifndef CS_LAUNCHMINT_LINUX_H
#define CS_LAUNCHMINT_LINUX_H

#include <stdint.h>

/* Credential kinds; mirrored by internal/launch's Kind* strings. */
enum {
	CS_MINT_NONE = 0,
	CS_MINT_X11_SN = 1,
	CS_MINT_WAYLAND_GDK = 2,
	CS_MINT_WAYLAND_XDG = 3
};

typedef struct {
	char *id; /* g_malloc'd; free with cs_mint_free */
	int kind; /* CS_MINT_* */
} CsMintResult;

/* 1 when the calling thread owns the default GLib main context (the
 * GTK main thread). */
int cs_on_gtk_thread(void);

/* Queue the Go closure behind handle onto the GTK main loop
 * (g_idle_add); csRunOnGtk runs and frees it. */
void cs_idle_add(uintptr_t handle);

/* Mint one launch credential. GTK main thread only. */
void cs_mint(CsMintResult *out);

/* One-time Wayland preparation (the keyboard serial listener). GTK
 * main thread only; no-op elsewhere and on X11. */
void cs_prepare_wayland(void);

/* Free a CsMintResult id (NULL-safe). */
void cs_mint_free(char *id);

#endif /* CS_LAUNCHMINT_LINUX_H */
