//go:build linux

/*
 * Launch-credential minting on the GTK main thread.
 *
 * Everything here runs on the thread that owns the default GLib main
 * context (wails runs gtk_main there); Go dispatches into it through
 * cs_idle_add + the exported csRunOnGtk trampoline (the wails
 * invoke.go pattern). Backends, decided by the live GDK display:
 *
 *  - X11: gdk's app-launch context mints a libstartup-notification id
 *    ("prgname-pid-host-...-seq_TIME<user-time>") and broadcasts the
 *    "new:" message itself. The _TIME suffix is gdk's cached last
 *    user-input time, which is the Enter press that triggered the
 *    launch -- the bar is focused when it fires.
 *  - Wayland + GTK 3.24.33 (Ubuntu 22.04): the same call mints a
 *    random uuid announced via gtk_shell1.notify_launch -- GNOME
 *    only (gtk_shell version >= 3); mutter registers it as a startup
 *    sequence timestamped "now", redeemable through BOTH the
 *    gtk_surface1.request_focus and xdg-activation paths.
 *  - Wayland + GTK >= 3.24.35: the same call mints a real
 *    xdg-activation token. Same environment plumbing either way.
 *  - Wayland, non-GNOME compositor (gdk returns NULL): bind
 *    xdg_activation_v1 ourselves on a DEDICATED event queue (proxy
 *    wrapper; never dispatch gdk's queue from here) and request a
 *    token authenticated by the last wl_keyboard serial our own
 *    listener saw (cs_prepare_wayland) and our toplevel's live
 *    wl_surface -- both fetched at mint time, because gdk destroys
 *    and recreates the xdg surface objects on every hide/show.
 *
 * Only symbols present at GTK 3.24.33 / GLib 2.72 are used (the
 * Ubuntu 22.04 runtime floor).
 */

#include <string.h>
#include <unistd.h>

#include <gtk/gtk.h>
#include <gio/gdesktopappinfo.h>

#ifdef GDK_WINDOWING_X11
#include <gdk/gdkx.h>
#endif
#ifdef GDK_WINDOWING_WAYLAND
#include <gdk/gdkwayland.h>
#include <wayland-client.h>
#include "xdg-activation-v1-client-protocol.h"
#endif

#include "launchmint_linux.h"
#include "_cgo_export.h"

int
cs_on_gtk_thread(void)
{
	return g_main_context_is_owner(g_main_context_default()) ? 1 : 0;
}

static gboolean
cs_idle_tramp(gpointer data)
{
	csRunOnGtk((uintptr_t)data);
	return G_SOURCE_REMOVE;
}

void
cs_idle_add(uintptr_t handle)
{
	g_idle_add(cs_idle_tramp, (gpointer)handle);
}

void
cs_mint_free(char *id)
{
	g_free(id);
}

/* cs_find_toplevel picks our bar window from the live GTK toplevels.
 * In this app exactly one real toplevel exists (no menus, devtools
 * compiled out under the production tag, WebKit popups are
 * GTK_WINDOW_POPUP); prefer the active one, then the app title, and
 * log if the assumption ever breaks. */
static GtkWindow *
cs_find_toplevel(void)
{
	GList *tops = gtk_window_list_toplevels();
	GtkWindow *best = NULL;
	int count = 0;
	GList *l;

	for (l = tops; l != NULL; l = l->next) {
		GtkWindow *w;
		const char *title;

		if (!GTK_IS_WINDOW(l->data))
			continue;
		w = GTK_WINDOW(l->data);
		if (gtk_window_get_window_type(w) != GTK_WINDOW_TOPLEVEL)
			continue;
		count++;
		if (best == NULL) {
			best = w;
			continue;
		}
		if (gtk_window_is_active(best))
			continue;
		if (gtk_window_is_active(w)) {
			best = w;
			continue;
		}
		title = gtk_window_get_title(w);
		if (title != NULL && strcmp(title, "competent-search-thing") == 0)
			best = w;
	}
	g_list_free(tops);
	if (count > 1)
		g_message("launch: %d toplevel gtk windows; minting against the active/titled one", count);
	return best;
}

/* cs_set_window_size resizes the bar window to w x h, moving the
 * non-resizable window's fixed-size floor with it. For
 * resizable=FALSE windows, GTK3 pins the geometry hints to
 * min = max = MAX(default size, configure request) on every
 * move-resize (gtk_window_update_fixed_size, gtk-3-24 gtkwindow.c),
 * so a bare gtk_window_resize -- what the Wails runtime's
 * WindowSetSize issues -- can grow the window but never shrink it
 * below the construction-time default. Updating the default FIRST
 * moves that floor; the resize then lands exactly. GTK main thread
 * only (the Go wrapper dispatches). Returns 1 when a toplevel was
 * found and asked to resize. */
int
cs_set_window_size(int w, int h)
{
	GtkWindow *top = cs_find_toplevel();
	if (top == NULL)
		return 0;
	gtk_window_set_default_size(top, w, h);
	gtk_window_resize(top, w, h);
	return 1;
}

/* cs_mint_app_info builds the GAppInfo the mint describes the launch
 * with: the resolved handler's desktop entry when we have one, else a
 * synthesized commandline entry flagged SUPPORTS_STARTUP_NOTIFICATION.
 * A real info is REQUIRED: GLib >= 2.76 asserts G_IS_APP_INFO(info)
 * in g_app_launch_context_get_startup_notify_id and returns NULL for
 * a NULL info (verified empirically on 2.80), and a desktop-entry
 * info also gives the X11 "new:" broadcast its proper NAME/WMCLASS
 * fields. */
static GAppInfo *
cs_mint_app_info(const char *desktop_id)
{
	GAppInfo *info = NULL;

	if (desktop_id != NULL && desktop_id[0] != '\0') {
		GDesktopAppInfo *dai = g_desktop_app_info_new(desktop_id);
		if (dai != NULL)
			info = G_APP_INFO(dai);
	}
	if (info == NULL)
		info = g_app_info_create_from_commandline("true",
		    "competent-search-thing-launch",
		    G_APP_INFO_CREATE_SUPPORTS_STARTUP_NOTIFICATION, NULL);
	return info;
}

/* cs_gdk_mint asks gdk's app-launch context for a startup-notify id,
 * described by the handler's appinfo. On X11 this also performs the
 * libsn "new:" broadcast. */
static char *
cs_gdk_mint(GdkDisplay *dpy, const char *desktop_id)
{
	GdkAppLaunchContext *ctx;
	GAppInfo *info;
	char *id;

	ctx = gdk_display_get_app_launch_context(dpy);
	if (ctx == NULL)
		return NULL;
	info = cs_mint_app_info(desktop_id);
	if (info == NULL) {
		g_object_unref(ctx);
		return NULL;
	}
	id = g_app_launch_context_get_startup_notify_id(G_APP_LAUNCH_CONTEXT(ctx), info, NULL);
	g_object_unref(info);
	g_object_unref(ctx);
	return id;
}

#ifdef GDK_WINDOWING_WAYLAND

/* Input-serial bookkeeping for the hand-rolled xdg-activation tier.
 * All access happens on the GTK thread: the listener callbacks are
 * dispatched by gdk's own main-loop source (the keyboard proxy lives
 * on the default queue), and cs_mint runs there too. */
static struct {
	struct wl_keyboard *kbd;
	uint32_t enter_serial;
	uint32_t press_serial;
	int prepared;
} cs_wl_state;

static void
cs_kbd_keymap(void *data, struct wl_keyboard *kbd, uint32_t format, int32_t fd, uint32_t size)
{
	(void)data; (void)kbd; (void)format; (void)size;
	close(fd); /* every keymap event carries an fd we must not leak */
}

static void
cs_kbd_enter(void *data, struct wl_keyboard *kbd, uint32_t serial,
             struct wl_surface *surface, struct wl_array *keys)
{
	(void)data; (void)kbd; (void)surface; (void)keys;
	cs_wl_state.enter_serial = serial;
}

static void
cs_kbd_leave(void *data, struct wl_keyboard *kbd, uint32_t serial, struct wl_surface *surface)
{
	(void)data; (void)kbd; (void)serial; (void)surface;
}

static void
cs_kbd_key(void *data, struct wl_keyboard *kbd, uint32_t serial, uint32_t time,
           uint32_t key, uint32_t state)
{
	(void)data; (void)kbd; (void)time; (void)key;
	if (state == WL_KEYBOARD_KEY_STATE_PRESSED)
		cs_wl_state.press_serial = serial;
}

static void
cs_kbd_modifiers(void *data, struct wl_keyboard *kbd, uint32_t serial,
                 uint32_t depressed, uint32_t latched, uint32_t locked, uint32_t group)
{
	(void)data; (void)kbd; (void)serial;
	(void)depressed; (void)latched; (void)locked; (void)group;
}

static void
cs_kbd_repeat_info(void *data, struct wl_keyboard *kbd, int32_t rate, int32_t delay)
{
	(void)data; (void)kbd; (void)rate; (void)delay;
}

static const struct wl_keyboard_listener cs_kbd_listener = {
	.keymap = cs_kbd_keymap,
	.enter = cs_kbd_enter,
	.leave = cs_kbd_leave,
	.key = cs_kbd_key,
	.modifiers = cs_kbd_modifiers,
	.repeat_info = cs_kbd_repeat_info,
};

struct cs_registry_ctx {
	struct xdg_activation_v1 *activation;
};

static void
cs_reg_global(void *data, struct wl_registry *reg, uint32_t name,
              const char *iface, uint32_t version)
{
	struct cs_registry_ctx *ctx = data;

	(void)version;
	if (ctx->activation == NULL && strcmp(iface, xdg_activation_v1_interface.name) == 0)
		ctx->activation = wl_registry_bind(reg, name, &xdg_activation_v1_interface, 1);
}

static void
cs_reg_global_remove(void *data, struct wl_registry *reg, uint32_t name)
{
	(void)data; (void)reg; (void)name;
}

static const struct wl_registry_listener cs_registry_listener = {
	.global = cs_reg_global,
	.global_remove = cs_reg_global_remove,
};

struct cs_token_ctx {
	char *token;
	int done;
};

static void
cs_token_done(void *data, struct xdg_activation_token_v1 *tok, const char *token)
{
	struct cs_token_ctx *ctx = data;

	(void)tok;
	if (ctx->token == NULL)
		ctx->token = g_strdup(token);
	ctx->done = 1;
}

static const struct xdg_activation_token_v1_listener cs_token_listener = {
	.done = cs_token_done,
};

/* cs_mint_xdg is the hand-rolled tier: bind xdg_activation_v1 on a
 * dedicated queue and request a token. The roundtrips block the GTK
 * thread briefly (a hung compositor hangs the whole session anyway);
 * events for gdk's default queue merely queue up meanwhile. */
static void
cs_mint_xdg(GdkDisplay *gdpy, CsMintResult *out)
{
	struct wl_display *wldpy;
	struct wl_event_queue *queue = NULL;
	struct wl_display *wrapped = NULL;
	struct wl_registry *registry = NULL;
	struct xdg_activation_token_v1 *token = NULL;
	struct cs_registry_ctx rctx = {0};
	struct cs_token_ctx tctx = {0};
	uint32_t serial;
	GdkSeat *gseat;
	struct wl_seat *seat;
	GtkWindow *top;
	int i;

	wldpy = gdk_wayland_display_get_wl_display(gdpy);
	if (wldpy == NULL)
		return;
	queue = wl_display_create_queue(wldpy);
	if (queue == NULL)
		return;
	wrapped = wl_proxy_create_wrapper(wldpy);
	if (wrapped == NULL)
		goto cleanup;
	wl_proxy_set_queue((struct wl_proxy *)wrapped, queue);
	registry = wl_display_get_registry(wrapped);
	if (registry == NULL)
		goto cleanup;
	wl_registry_add_listener(registry, &cs_registry_listener, &rctx);
	if (wl_display_roundtrip_queue(wldpy, queue) < 0)
		goto cleanup;
	if (rctx.activation == NULL)
		goto cleanup; /* compositor without xdg-activation: no credential */

	token = xdg_activation_v1_get_activation_token(rctx.activation);
	if (token == NULL)
		goto cleanup;
	xdg_activation_token_v1_add_listener(token, &cs_token_listener, &tctx);

	serial = cs_wl_state.press_serial != 0 ? cs_wl_state.press_serial : cs_wl_state.enter_serial;
	gseat = gdk_display_get_default_seat(gdpy);
	seat = gseat != NULL ? gdk_wayland_seat_get_wl_seat(gseat) : NULL;
	if (seat != NULL && serial != 0)
		xdg_activation_token_v1_set_serial(token, serial, seat);

	/* Fetch the surface NOW: gdk destroys the wayland objects on
	 * every hide and recreates them on map, so a cached pointer
	 * would dangle. */
	top = cs_find_toplevel();
	if (top != NULL) {
		GdkWindow *gw = gtk_widget_get_window(GTK_WIDGET(top));
		struct wl_surface *surf =
		    gw != NULL ? gdk_wayland_window_get_wl_surface(gw) : NULL;
		if (surf != NULL)
			xdg_activation_token_v1_set_surface(token, surf);
	}
	xdg_activation_token_v1_set_app_id(token, "competent-search-thing");
	xdg_activation_token_v1_commit(token);

	for (i = 0; i < 2 && !tctx.done; i++) {
		if (wl_display_roundtrip_queue(wldpy, queue) < 0)
			break;
	}
	if (tctx.done && tctx.token != NULL && tctx.token[0] != '\0') {
		out->id = tctx.token;
		out->kind = CS_MINT_WAYLAND_XDG;
	} else {
		g_free(tctx.token);
	}

cleanup:
	if (token != NULL)
		xdg_activation_token_v1_destroy(token);
	if (rctx.activation != NULL)
		xdg_activation_v1_destroy(rctx.activation);
	if (registry != NULL)
		wl_registry_destroy(registry);
	if (wrapped != NULL)
		wl_proxy_wrapper_destroy(wrapped);
	wl_event_queue_destroy(queue);
}

#endif /* GDK_WINDOWING_WAYLAND */

void
cs_prepare_wayland(void)
{
#ifdef GDK_WINDOWING_WAYLAND
	GdkDisplay *dpy;
	GdkSeat *gseat;
	struct wl_seat *seat;

	if (cs_wl_state.prepared)
		return;
	cs_wl_state.prepared = 1;
	dpy = gdk_display_get_default();
	if (dpy == NULL || !GDK_IS_WAYLAND_DISPLAY(dpy))
		return;
	gseat = gdk_display_get_default_seat(dpy);
	if (gseat == NULL || (gdk_seat_get_capabilities(gseat) & GDK_SEAT_CAPABILITY_KEYBOARD) == 0)
		return;
	seat = gdk_wayland_seat_get_wl_seat(gseat);
	if (seat == NULL)
		return;
	/* Our own keyboard object on gdk's seat: events ride the default
	 * queue, so gdk's main-loop source dispatches our listener on
	 * this thread. */
	cs_wl_state.kbd = wl_seat_get_keyboard(seat);
	if (cs_wl_state.kbd != NULL)
		wl_keyboard_add_listener(cs_wl_state.kbd, &cs_kbd_listener, NULL);
#endif
}

void
cs_mint(const char *desktop_id, CsMintResult *out)
{
	GdkDisplay *dpy;
	char *id;

	out->id = NULL;
	out->kind = CS_MINT_NONE;
	dpy = gdk_display_get_default();
	if (dpy == NULL)
		return;
#ifdef GDK_WINDOWING_X11
	if (GDK_IS_X11_DISPLAY(dpy)) {
		id = cs_gdk_mint(dpy, desktop_id);
		if (id != NULL) {
			out->id = id;
			out->kind = CS_MINT_X11_SN;
		}
		return;
	}
#endif
#ifdef GDK_WINDOWING_WAYLAND
	if (GDK_IS_WAYLAND_DISPLAY(dpy)) {
		id = cs_gdk_mint(dpy, desktop_id);
		if (id != NULL) {
			out->id = id;
			out->kind = CS_MINT_WAYLAND_GDK;
			return;
		}
		cs_prepare_wayland(); /* idempotent; normally done at Startup */
		cs_mint_xdg(dpy, out);
		return;
	}
#endif
	(void)id;
	(void)desktop_id;
}
