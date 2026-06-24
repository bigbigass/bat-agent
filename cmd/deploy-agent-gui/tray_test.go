package main

import "testing"

func TestTrayControllerCloseHidesWindow(t *testing.T) {
	hidden := false
	closed := false
	controller := trayController{
		hide:  func() { hidden = true },
		close: func() { closed = true },
	}

	controller.interceptClose()

	if !hidden {
		t.Fatal("hidden = false, want true")
	}
	if closed {
		t.Fatal("closed = true, want false while not exiting")
	}
}

func TestTrayControllerExitClosesWindowAndRunsShutdown(t *testing.T) {
	closed := false
	shutdown := false
	controller := trayController{
		close:    func() { closed = true },
		shutdown: func() { shutdown = true },
	}

	controller.exit()

	if !controller.exiting {
		t.Fatal("exiting = false, want true")
	}
	if !shutdown {
		t.Fatal("shutdown = false, want true")
	}
	if !closed {
		t.Fatal("closed = false, want true")
	}
}

func TestTrayControllerOpenShowsAndFocusesWindow(t *testing.T) {
	shown := false
	focused := false
	controller := trayController{
		show:  func() { shown = true },
		focus: func() { focused = true },
	}

	controller.open()

	if !shown {
		t.Fatal("shown = false, want true")
	}
	if !focused {
		t.Fatal("focused = false, want true")
	}
}
