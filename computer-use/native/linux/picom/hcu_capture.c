// The compositor owns the scene and its stacking. Export an on-demand
// BGRX crop in a sealed memfd, with no writes to the visible framebuffer.
#include "hcu_capture.h"

#include <endian.h>
#include <errno.h>
#include <ev.h>
#include <fcntl.h>
#include <limits.h>
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/file.h>
#include <sys/mman.h>
#include <sys/random.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <unistd.h>
#include <time.h>
#include <xcb/xcbext.h>
#include <xcb/shm.h>

#include "common.h"
#include "log.h"
#include "picom.h"
#include "render.h"
#include "x.h"

enum { HCU_PROTOCOL = 2, HCU_MAX_PIXELS = 16 * 1024 * 1024, HCU_BATCH = 12 };
static const char session_property[] = "_HCU_CLEAN_CAPTURE_SESSION";

struct hcu_wire_request {
	uint32_t version, x, y, width, height;
	uint8_t session[16];
	uint32_t deadline_low, deadline_high;
};

struct hcu_crop {
	uint32_t x, y, width, height;
};

struct hcu_request {
	struct sockaddr_un peer;
	socklen_t peer_length;
	struct hcu_crop crop;
	uint64_t deadline;
};

struct hcu_capture {
	ev_io io;
	ev_timer batch_timer, reply_timer;
	int lock_fd;
	xcb_atom_t marker;
	uint8_t session[16];
	struct hcu_request waiting[HCU_BATCH], active[HCU_BATCH];
	size_t waiting_count, active_count;
	struct hcu_crop frame;
	unsigned reply_sequence, detach_sequence;
	int shared_fd;
	xcb_pixmap_t snapshot;
	xcb_shm_seg_t segment;
	uint32_t rows_read, band_rows;
	bool shared_supported, shared_complete;
	bool draw_requested;
	double started, next_batch;
	char path[sizeof(((struct sockaddr_un *)0)->sun_path)];
};

static uint64_t monotonic_ns(void) {
	struct timespec now;
	clock_gettime(CLOCK_MONOTONIC, &now);
	return (uint64_t)now.tv_sec * 1000000000 + (uint64_t)now.tv_nsec;
}

static double monotonic_seconds(void) {
	return (double)monotonic_ns() / 1e9;
}

static void send_frame(struct hcu_capture *capture, const struct hcu_request *request,
                       int fd, struct hcu_crop crop) {
	uint32_t header[] = {htole32(HCU_PROTOCOL), htole32(crop.x), htole32(crop.y),
	                     htole32(crop.width), htole32(crop.height)};
	struct iovec body[] = {
	    {.iov_base = header, .iov_len = sizeof(header)},
	    {.iov_base = capture->session, .iov_len = sizeof(capture->session)},
	};
	union {
		struct cmsghdr align;
		char buffer[CMSG_SPACE(sizeof(fd))];
	} control;
	struct msghdr message = {
	    .msg_name = (void *)&request->peer,
	    .msg_namelen = request->peer_length,
	    .msg_iov = body,
	    .msg_iovlen = 2,
	};
	if (fd >= 0) {
		message.msg_control = control.buffer;
		message.msg_controllen = sizeof(control.buffer);
		struct cmsghdr *cmsg = CMSG_FIRSTHDR(&message);
		cmsg->cmsg_level = SOL_SOCKET;
		cmsg->cmsg_type = SCM_RIGHTS;
		cmsg->cmsg_len = CMSG_LEN(sizeof(fd));
		memcpy(CMSG_DATA(cmsg), &fd, sizeof(fd));
	}
	sendmsg(capture->io.fd, &message, MSG_DONTWAIT | MSG_NOSIGNAL);
}

static void queue_batch(session_t *ps) {
	struct hcu_capture *capture = ps->hcu_capture;
	if (!capture->waiting_count || capture->active_count ||
	    capture->draw_requested || ev_is_active(&capture->batch_timer))
		return;
	// Give simultaneous requests a chance to share a render. Cooldown keeps
	// capture batches from immediately following one another under load.
	double delay = capture->next_batch - monotonic_seconds();
	ev_timer_set(&capture->batch_timer, delay > .004 ? delay : .004, 0.);
	ev_timer_start(ps->loop, &capture->batch_timer);
}

static void begin_batch(EV_P_ ev_timer *watcher, int events attr_unused) {
	session_t *ps = watcher->data;
	ps->hcu_capture->draw_requested = true;
	ps->pending_updates = true;
	queue_redraw(ps);
}

static void receive_request(EV_P_ ev_io *watcher, int events attr_unused) {
	session_t *ps = watcher->data;
	struct hcu_capture *capture = ps->hcu_capture;
	struct hcu_wire_request wire;
	struct hcu_request request = {0};
	union {
		struct cmsghdr align;
		char buffer[CMSG_SPACE(sizeof(struct ucred))];
	} control;
	struct iovec body = {.iov_base = &wire, .iov_len = sizeof(wire)};
	struct msghdr message = {
	    .msg_name = &request.peer,
	    .msg_namelen = sizeof(request.peer),
	    .msg_iov = &body,
	    .msg_iovlen = 1,
	    .msg_control = control.buffer,
	    .msg_controllen = sizeof(control.buffer),
	};
	ssize_t received = recvmsg(watcher->fd, &message, MSG_CMSG_CLOEXEC);
	if (received < 0)
		return;
	bool same_user = false;
	for (struct cmsghdr *cmsg = CMSG_FIRSTHDR(&message); cmsg;
	     cmsg = CMSG_NXTHDR(&message, cmsg)) {
		if (cmsg->cmsg_level != SOL_SOCKET)
			continue;
		if (cmsg->cmsg_type == SCM_CREDENTIALS &&
		    cmsg->cmsg_len == CMSG_LEN(sizeof(struct ucred))) {
			struct ucred cred;
			memcpy(&cred, CMSG_DATA(cmsg), sizeof(cred));
			same_user = cred.uid == geteuid();
		}
		if (cmsg->cmsg_type == SCM_RIGHTS && cmsg->cmsg_len >= CMSG_LEN(0)) {
			for (size_t offset = 0; offset + sizeof(int) <= cmsg->cmsg_len - CMSG_LEN(0);
			     offset += sizeof(int)) {
				int unwanted;
				memcpy(&unwanted, CMSG_DATA(cmsg) + offset, sizeof(unwanted));
				close(unwanted);
			}
		}
	}
	if (received != (ssize_t)sizeof(wire) || !same_user ||
	    (message.msg_flags & (MSG_TRUNC | MSG_CTRUNC)) ||
	    message.msg_namelen <= offsetof(struct sockaddr_un, sun_path) ||
	    message.msg_namelen > sizeof(request.peer))
		return;
	request.peer_length = message.msg_namelen;
	request.crop = (struct hcu_crop){le32toh(wire.x), le32toh(wire.y),
	                                 le32toh(wire.width), le32toh(wire.height)};
	request.deadline = ((uint64_t)le32toh(wire.deadline_high) << 32) |
	                   le32toh(wire.deadline_low);
	uint64_t now = monotonic_ns();
	if (le32toh(wire.version) != HCU_PROTOCOL ||
	    memcmp(wire.session, capture->session, sizeof(wire.session)) != 0 ||
	    request.deadline <= now || request.deadline - now > 1000000000 ||
	    capture->waiting_count == HCU_BATCH) {
		send_frame(capture, &request, -1, (struct hcu_crop){0});
		return;
	}
	capture->waiting[capture->waiting_count++] = request;
	if (capture->waiting_count == HCU_BATCH)
		ev_io_stop(EV_A_ watcher);
	queue_batch(ps);
}

static bool private_parent(const char *path) {
	if (path[0] != '/' || strlen(path) >= sizeof(((struct sockaddr_un *)0)->sun_path))
		return false;
	char parent[sizeof(((struct sockaddr_un *)0)->sun_path)];
	strcpy(parent, path);
	*strrchr(parent, '/') = '\0';
	struct stat info;
	return stat(parent, &info) == 0 && S_ISDIR(info.st_mode) &&
	       info.st_uid == geteuid() && (info.st_mode & 077) == 0;
}

static int lock_endpoint(const struct sockaddr_un *address) {
	char path[sizeof(address->sun_path) + sizeof(".lock")];
	strcpy(path, address->sun_path);
	strcat(path, ".lock");
	int fd = open(path, O_CREAT | O_RDWR | O_NOFOLLOW | O_CLOEXEC, 0600);
	if (fd < 0)
		return -1;
	struct stat info;
	if (fstat(fd, &info) || !S_ISREG(info.st_mode) || info.st_uid != geteuid() ||
	    (info.st_mode & 077) || flock(fd, LOCK_EX | LOCK_NB)) {
		close(fd);
		return -1;
	}
	// A forced restart can leave a socket inode. Preserve unrelated or live
	// endpoints, even if their owners do not use this lock.
	if (lstat(address->sun_path, &info) == 0) {
		int probe = socket(AF_UNIX, SOCK_DGRAM | SOCK_CLOEXEC, 0);
		bool stale = probe >= 0 && S_ISSOCK(info.st_mode) && info.st_uid == geteuid() &&
		             connect(probe, (const struct sockaddr *)address, sizeof(*address)) < 0 &&
		             errno == ECONNREFUSED;
		if (probe >= 0)
			close(probe);
		if (!stale || unlink(address->sun_path)) {
			close(fd);
			return -1;
		}
	}
	return fd;
}

static void poll_reply(EV_P_ ev_timer *watcher, int events attr_unused);

static bool publish_session(session_t *ps, struct hcu_capture *capture) {
	if (getrandom(capture->session, sizeof(capture->session), 0) !=
	    (ssize_t)sizeof(capture->session))
		return false;
	xcb_intern_atom_reply_t *atom = xcb_intern_atom_reply(
	    ps->c, xcb_intern_atom(ps->c, 0, sizeof(session_property) - 1, session_property), NULL);
	if (!atom)
		return false;
	xcb_generic_error_t *error = xcb_request_check(
	    ps->c, xcb_change_property_checked(ps->c, XCB_PROP_MODE_REPLACE, ps->root,
	                                       atom->atom, XCB_ATOM_STRING, 8,
	                                       sizeof(capture->session), capture->session));
	free(atom);
	if (error) {
		free(error);
		return false;
	}
	return true;
}

static bool local_shm_supported(xcb_connection_t *connection) {
	struct sockaddr_storage address;
	socklen_t length = sizeof(address);
	const xcb_query_extension_reply_t *extension = xcb_get_extension_data(connection, &xcb_shm_id);
	if (!extension || !extension->present ||
	    getsockname(xcb_get_file_descriptor(connection), (struct sockaddr *)&address, &length) ||
	    address.ss_family != AF_UNIX)
		return false;
	xcb_shm_query_version_reply_t *version =
	    xcb_shm_query_version_reply(connection, xcb_shm_query_version(connection), NULL);
	bool supported = version &&
	                 (version->major_version > 1 ||
	                  (version->major_version == 1 && version->minor_version >= 2));
	free(version);
	return supported;
}

void hcu_capture_start(session_t *ps) {
	const char *path = getenv("HCU_CLEAN_CAPTURE_SOCKET");
	if (!path || !path[0])
		return;
	if (ps->o.backend != BKEND_XRENDER || ps->o.experimental_backends ||
	    ps->depth != 24 || !private_parent(path)) {
		log_error("Clean capture requires legacy 24-bit XRender and a private socket directory");
		return;
	}
	const xcb_setup_t *setup = xcb_get_setup(ps->c);
	const xcb_render_pictforminfo_t *format = x_get_pictform_for_visual(ps->c, ps->vis);
	if (setup->image_byte_order != XCB_IMAGE_ORDER_LSB_FIRST || !format ||
	    format->type != XCB_RENDER_PICT_TYPE_DIRECT || format->direct.red_shift != 16 ||
	    format->direct.green_shift != 8 || format->direct.blue_shift != 0 ||
	    format->direct.red_mask != 255 || format->direct.green_mask != 255 ||
	    format->direct.blue_mask != 255) {
		log_error("Clean capture requires the BGRX visual");
		return;
	}
	const char marker[] = "_HCU_CURSOR_OVERLAY";
	xcb_intern_atom_reply_t *atom =
	    xcb_intern_atom_reply(ps->c, xcb_intern_atom(ps->c, 0, sizeof(marker) - 1, marker), NULL);
	if (!atom)
		return;
	xcb_atom_t marker_id = atom->atom;
	free(atom);

	int fd = socket(AF_UNIX, SOCK_DGRAM | SOCK_CLOEXEC | SOCK_NONBLOCK, 0);
	if (fd < 0) {
		log_error("Clean capture socket creation failed: %s", strerror(errno));
		return;
	}
	struct sockaddr_un address = {.sun_family = AF_UNIX};
	strcpy(address.sun_path, path);
	int lock_fd = lock_endpoint(&address);
	if (lock_fd < 0) {
		log_error("Clean capture endpoint is unavailable");
		close(fd);
		return;
	}
	int enabled = 1;
	if (setsockopt(fd, SOL_SOCKET, SO_PASSCRED, &enabled, sizeof(enabled)) ||
	    bind(fd, (struct sockaddr *)&address, sizeof(address))) {
		log_error("Clean capture socket setup failed: %s", strerror(errno));
		close(fd);
		close(lock_fd);
		return;
	}
	struct hcu_capture *capture = calloc(1, sizeof(*capture));
	if (!capture || !publish_session(ps, capture)) {
		free(capture);
		close(fd);
		unlink(path);
		close(lock_fd);
		return;
	}
	strcpy(capture->path, path);
	capture->marker = marker_id;
	capture->lock_fd = lock_fd;
	capture->shared_fd = -1;
	capture->shared_supported = local_shm_supported(ps->c);
	ps->hcu_capture = capture;
	ev_io_init(&capture->io, receive_request, fd, EV_READ);
	capture->io.data = ps;
	ev_timer_init(&capture->batch_timer, begin_batch, 0., 0.);
	capture->batch_timer.data = ps;
	ev_timer_init(&capture->reply_timer, poll_reply, .001, .001);
	capture->reply_timer.data = ps;
	ev_io_start(ps->loop, &capture->io);
}

static void release_snapshot(session_t *ps) {
	struct hcu_capture *capture = ps->hcu_capture;
	if (capture->segment != XCB_NONE) {
		xcb_shm_detach(ps->c, capture->segment);
		capture->segment = XCB_NONE;
	}
	if (capture->snapshot != XCB_NONE) {
		xcb_free_pixmap(ps->c, capture->snapshot);
		capture->snapshot = XCB_NONE;
	}
}

void hcu_capture_stop(session_t *ps) {
	struct hcu_capture *capture = ps->hcu_capture;
	if (!capture)
		return;
	ev_io_stop(ps->loop, &capture->io);
	ev_timer_stop(ps->loop, &capture->batch_timer);
	ev_timer_stop(ps->loop, &capture->reply_timer);
	if (capture->reply_sequence)
		xcb_discard_reply(ps->c, capture->reply_sequence);
	if (capture->detach_sequence)
		xcb_discard_reply(ps->c, capture->detach_sequence);
	release_snapshot(ps);
	if (capture->shared_fd >= 0)
		close(capture->shared_fd);
	close(capture->io.fd);
	unlink(capture->path);
	close(capture->lock_fd);
	free(capture);
	ps->hcu_capture = NULL;
}

bool hcu_capture_is_overlay(session_t *ps, xcb_window_t window) {
	if (!ps->hcu_capture)
		return false;
	xcb_get_property_reply_t *reply = xcb_get_property_reply(
	    ps->c, xcb_get_property(ps->c, 0, window, ps->hcu_capture->marker,
	                            XCB_ATOM_CARDINAL, 0, 1), NULL);
	bool overlay = false;
	if (reply && reply->type == XCB_ATOM_CARDINAL && reply->format == 32 &&
	    xcb_get_property_value_length(reply) == 4) {
		uint32_t kind;
		memcpy(&kind, xcb_get_property_value(reply), sizeof(kind));
		overlay = kind == 1 || kind == 2;
	}
	free(reply);
	return overlay;
}

static int export_bgrx(const xcb_get_image_reply_t *image, size_t size) {
	if (!image || (size_t)xcb_get_image_data_length(image) != size)
		return -1;
	int fd = memfd_create("hcu-clean-frame", MFD_CLOEXEC | MFD_ALLOW_SEALING);
	if (fd < 0)
		return -1;
	// A single immutable buffer is shared by every reader in the batch.
	if (write(fd, xcb_get_image_data(image), size) == (ssize_t)size &&
	    fcntl(fd, F_ADD_SEALS, F_SEAL_SHRINK | F_SEAL_GROW | F_SEAL_WRITE | F_SEAL_SEAL) == 0)
		return fd;
	close(fd);
	return -1;
}

static void finish_batch(session_t *ps, int fd) {
	struct hcu_capture *capture = ps->hcu_capture;
	uint64_t now = monotonic_ns();
	for (size_t i = 0; i < capture->active_count; i++) {
		struct hcu_request *request = &capture->active[i];
		bool valid = fd >= 0 && request->deadline > now;
		send_frame(capture, request, valid ? fd : -1,
		           valid ? capture->frame : (struct hcu_crop){0});
	}
	if (fd >= 0)
		close(fd);
	if (capture->shared_fd >= 0 && capture->shared_fd != fd)
		close(capture->shared_fd);
	capture->shared_fd = -1;
	release_snapshot(ps);
	capture->active_count = 0;
	capture->reply_sequence = 0;
	capture->detach_sequence = 0;
	capture->rows_read = 0;
	capture->shared_complete = false;
	double finished = monotonic_seconds();
	// Reserve at least three capture-cost intervals for normal desktop work,
	// with a fixed minimum gap even on small crops.
	double cooldown = 3 * (finished - capture->started);
	capture->next_batch = finished + (cooldown > .024 ? cooldown : .024);
	queue_batch(ps);
}

static bool has_live_reader(const struct hcu_capture *capture) {
	uint64_t now = monotonic_ns();
	for (size_t i = 0; i < capture->active_count; i++)
		if (capture->active[i].deadline > now)
			return true;
	return false;
}

static void read_next_band(session_t *ps) {
	struct hcu_capture *capture = ps->hcu_capture;
	struct hcu_crop frame = capture->frame;
	// At most 2 MiB of BGRX per X11 read. The source pixmap is immutable,
	// so the compositor can update the visible frame between bands.
	uint32_t rows = (512 * 1024) / frame.width;
	uint32_t remaining = frame.height - capture->rows_read;
	capture->band_rows = rows < remaining ? rows : remaining;
	capture->reply_sequence = xcb_shm_get_image(
	    ps->c, capture->snapshot, (int16_t)frame.x, (int16_t)(frame.y + capture->rows_read),
	    (uint16_t)frame.width, (uint16_t)capture->band_rows, UINT32_MAX,
	    XCB_IMAGE_FORMAT_Z_PIXMAP, capture->segment, capture->rows_read * frame.width * 4).sequence;
	xcb_flush(ps->c);
}

static void finish_shared_readback(session_t *ps, bool complete) {
	struct hcu_capture *capture = ps->hcu_capture;
	capture->shared_complete = complete;
	release_snapshot(ps);
	capture->detach_sequence = xcb_get_input_focus(ps->c).sequence;
	xcb_flush(ps->c);
}

static void poll_reply(EV_P_ ev_timer *watcher, int events attr_unused) {
	session_t *ps = watcher->data;
	struct hcu_capture *capture = ps->hcu_capture;
	void *reply = NULL;
	xcb_generic_error_t *error = NULL;
	if (xcb_connection_has_error(ps->c)) {
		ev_timer_stop(EV_A_ watcher);
		finish_batch(ps, -1);
		return;
	}
	if (capture->detach_sequence) {
		// The server's writable mapping must be gone before sealing.
		if (!xcb_poll_for_reply(ps->c, capture->detach_sequence, &reply, &error))
			return;
		int fd = -1;
		if (!error && reply && capture->shared_complete && has_live_reader(capture) &&
		    fcntl(capture->shared_fd, F_ADD_SEALS,
		          F_SEAL_SHRINK | F_SEAL_GROW | F_SEAL_WRITE | F_SEAL_SEAL) == 0)
			fd = capture->shared_fd;
		free(reply);
		free(error);
		ev_timer_stop(EV_A_ watcher);
		finish_batch(ps, fd);
		return;
	}
	if (!capture->reply_sequence && capture->shared_fd >= 0) {
		if (has_live_reader(capture))
			read_next_band(ps);
		else
			finish_shared_readback(ps, false);
		return;
	}
	if (!xcb_poll_for_reply(ps->c, capture->reply_sequence, &reply, &error))
		return;
	capture->reply_sequence = 0;
	if (capture->shared_fd >= 0) {
		const xcb_shm_get_image_reply_t *image = reply;
		bool valid = !error && image && image->depth == 24 &&
		             image->size == capture->frame.width * capture->band_rows * 4;
		free(reply);
		free(error);
		if (!valid) {
			finish_shared_readback(ps, false);
		} else {
			capture->rows_read += capture->band_rows;
			if (capture->rows_read == capture->frame.height)
				finish_shared_readback(ps, true);
			// Leave the next band for a later event-loop turn.
		}
		return;
	}
	size_t size = (size_t)capture->frame.width * capture->frame.height * 4;
	int fd = !error && has_live_reader(capture) ? export_bgrx(reply, size) : -1;
	free(reply);
	free(error);
	ev_timer_stop(EV_A_ watcher);
	finish_batch(ps, fd);
}

static xcb_shm_seg_t prepare_shared_readback(session_t *ps, size_t size) {
	struct hcu_capture *capture = ps->hcu_capture;
	if (!capture->shared_supported)
		return XCB_NONE;
	int fd = memfd_create("hcu-clean-frame", MFD_CLOEXEC | MFD_ALLOW_SEALING);
	if (fd < 0)
		return XCB_NONE;
	int sent_fd = -1;
	xcb_shm_seg_t segment = xcb_generate_id(ps->c);
	if (segment != UINT32_MAX && ftruncate(fd, (off_t)size) == 0)
		sent_fd = fcntl(fd, F_DUPFD_CLOEXEC, 0);
	if (sent_fd < 0) {
		close(fd);
		return XCB_NONE;
	}
	// XCB owns the duplicate once submitted. We retain the descriptor that
	// will be sealed and shared with the screenshot clients.
	xcb_shm_attach_fd(ps->c, segment, sent_fd, 0);
	capture->shared_fd = fd;
	return segment;
}

static bool valid_crop(session_t *ps, struct hcu_crop crop) {
	return ps->redirected && ps->o.stoppaint_force != ON &&
	       ps->root_width > 0 && ps->root_height > 0 &&
	       (uint64_t)ps->root_width * (uint64_t)ps->root_height <= HCU_MAX_PIXELS &&
	       crop.x <= INT16_MAX && crop.y <= INT16_MAX &&
	       crop.width && crop.height && crop.width <= INT16_MAX && crop.height <= INT16_MAX &&
	       crop.x + crop.width <= (uint32_t)ps->root_width &&
	       crop.y + crop.height <= (uint32_t)ps->root_height;
}

void hcu_capture_frame(session_t *ps, struct managed_win *bottom) {
	struct hcu_capture *capture = ps->hcu_capture;
	if (!capture || !capture->draw_requested)
		return;
	capture->draw_requested = false;
	uint64_t now = monotonic_ns();
	uint32_t x1 = UINT32_MAX, y1 = UINT32_MAX, x2 = 0, y2 = 0;
	for (size_t i = 0; i < capture->waiting_count; i++) {
		struct hcu_request *request = &capture->waiting[i];
		struct hcu_crop crop = request->crop;
		if (request->deadline <= now || !valid_crop(ps, crop)) {
			send_frame(capture, request, -1, (struct hcu_crop){0});
			continue;
		}
		capture->active[capture->active_count++] = *request;
		if (crop.x < x1) x1 = crop.x;
		if (crop.y < y1) y1 = crop.y;
		if (crop.x + crop.width > x2) x2 = crop.x + crop.width;
		if (crop.y + crop.height > y2) y2 = crop.y + crop.height;
	}
	capture->waiting_count = 0;
	ev_io_start(ps->loop, &capture->io);
	if (!capture->active_count)
		return;

	capture->frame = (struct hcu_crop){x1, y1, x2 - x1, y2 - y1};
	capture->started = monotonic_seconds();
	capture->snapshot = paint_clean_crop(
	    ps, bottom, (int16_t)x1, (int16_t)y1, (uint16_t)(x2 - x1), (uint16_t)(y2 - y1));
	if (capture->snapshot == XCB_NONE) {
		finish_batch(ps, -1);
		return;
	}
	// Band origins are signed X11 coordinates. A single read can reach
	// farther than the last representable origin on a very tall screen.
	if (y2 <= (uint32_t)INT16_MAX + 1)
		capture->segment = prepare_shared_readback(
		    ps, (size_t)capture->frame.width * capture->frame.height * 4);
	if (capture->segment != XCB_NONE) {
		read_next_band(ps);
	} else {
		capture->reply_sequence = xcb_get_image(
		    ps->c, XCB_IMAGE_FORMAT_Z_PIXMAP, capture->snapshot, (int16_t)x1, (int16_t)y1,
		    (uint16_t)(x2 - x1), (uint16_t)(y2 - y1), UINT32_MAX).sequence;
		xcb_flush(ps->c);
	}
	ev_timer_again(ps->loop, &capture->reply_timer);
}
