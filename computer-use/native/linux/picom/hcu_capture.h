// Optional clean capture for the legacy XRender backend.
#pragma once

#include <stdbool.h>
#include <xcb/xcb.h>

struct session;
struct managed_win;

void hcu_capture_start(struct session *ps);
void hcu_capture_stop(struct session *ps);
void hcu_capture_frame(struct session *ps, struct managed_win *bottom);
bool hcu_capture_is_overlay(struct session *ps, xcb_window_t window);
