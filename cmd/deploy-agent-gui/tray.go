package main

import (
	"context"
	"log"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

type trayController struct {
	exiting  bool
	hide     func()
	show     func()
	focus    func()
	close    func()
	shutdown func()
	quit     func()
}

func (c *trayController) interceptClose() {
	if c.exiting {
		if c.close != nil {
			c.close()
		}
		return
	}
	if c.hide != nil {
		c.hide()
	}
}

func (c *trayController) open() {
	if c.show != nil {
		c.show()
	}
	if c.focus != nil {
		c.focus()
	}
}

func (c *trayController) exit() {
	if c.exiting {
		return
	}
	c.exiting = true
	if c.shutdown != nil {
		c.shutdown()
	}
	if c.close != nil {
		c.close()
	}
	if c.quit != nil {
		c.quit()
	}
}

func (s *guiState) installTray(a fyne.App, w fyne.Window) {
	controller := &trayController{
		hide:  w.Hide,
		show:  w.Show,
		focus: w.RequestFocus,
		close: func() {
			w.SetCloseIntercept(nil)
			w.Close()
		},
		shutdown: func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.service.Shutdown(ctx); err != nil {
				log.Printf("shutdown embedded service: %v", err)
			}
		},
		quit: a.Quit,
	}
	w.SetCloseIntercept(controller.interceptClose)

	desktopApp, ok := a.(desktop.App)
	if !ok {
		return
	}
	desktopApp.SetSystemTrayWindow(w)
	desktopApp.SetSystemTrayMenu(fyne.NewMenu("deploy-agent",
		fyne.NewMenuItem("打开", controller.open),
		fyne.NewMenuItem("退出", controller.exit),
	))
}
