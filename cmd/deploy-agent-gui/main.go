//go:build cgo

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/widget"
	"github.com/liqixin/deploy-agent/internal/gui/apiclient"
	"github.com/liqixin/deploy-agent/internal/gui/guiconfig"
	"github.com/liqixin/deploy-agent/internal/gui/localservice"
)

type guiState struct {
	configPath string
	config     guiconfig.Config
	client     *apiclient.Client
	service    *localservice.Manager

	statusText binding.String
	outputText binding.String
	history    binding.StringList

	scriptSelect *widget.Select
	runButton    *widget.Button
	startButton  *widget.Button
	stopButton   *widget.Button

	connectSeq int
	refreshSeq int
	running    bool
}

func main() {
	a := app.New()
	w := a.NewWindow("deploy-agent 管理控制台")
	w.Resize(fyne.NewSize(980, 680))

	cfgPath, err := guiconfig.Path()
	if err != nil {
		cfgPath = "deploy-agent-gui.json"
	}
	cfg, err := guiconfig.Load(cfgPath)
	if err != nil {
		cfg = guiconfig.Default()
	}

	exe, _ := os.Executable()
	state := &guiState{
		configPath: cfgPath,
		config:     cfg,
		service:    localservice.New(localservice.AgentPath(exe)),
		statusText: binding.NewString(),
		outputText: binding.NewString(),
		history:    binding.NewStringList(),
	}
	state.setStatus("未连接")

	w.SetContent(state.buildUI())
	w.ShowAndRun()
}

func (s *guiState) buildUI() fyne.CanvasObject {
	mode := widget.NewSelect([]string{guiconfig.ModeLocal, guiconfig.ModeRemote}, func(value string) {
		s.config.Mode = value
		s.updateLocalButtons()
	})

	baseURL := widget.NewEntry()
	baseURL.SetText(s.config.BaseURL)
	baseURL.OnChanged = func(value string) {
		s.config.BaseURL = strings.TrimSpace(value)
	}

	username := widget.NewEntry()
	username.SetText(s.config.Username)
	username.OnChanged = func(value string) {
		s.config.Username = value
	}

	password := widget.NewPasswordEntry()
	password.SetText(s.config.Password)
	password.OnChanged = func(value string) {
		s.config.Password = value
	}

	connect := widget.NewButton("连接", s.connect)
	save := widget.NewButton("保存配置", func() {
		if err := guiconfig.Save(s.configPath, s.config); err != nil {
			s.setStatus("保存配置失败: " + err.Error())
			return
		}
		s.setStatus("配置已保存")
	})
	s.startButton = widget.NewButton("启动服务", s.startLocalService)
	s.stopButton = widget.NewButton("停止服务", s.stopLocalService)

	s.scriptSelect = widget.NewSelect([]string{}, func(string) {})
	refresh := widget.NewButton("刷新脚本", s.refreshScripts)
	s.runButton = widget.NewButton("执行脚本", s.runSelectedScript)
	s.runButton.Disable()

	status := widget.NewLabelWithData(s.statusText)
	output := widget.NewMultiLineEntry()
	output.Bind(s.outputText)
	output.Wrapping = fyne.TextWrapOff
	output.Disable()

	history := widget.NewListWithData(
		s.history,
		func() fyne.CanvasObject {
			return widget.NewLabel("")
		},
		func(item binding.DataItem, obj fyne.CanvasObject) {
			text, _ := item.(binding.String).Get()
			obj.(*widget.Label).SetText(text)
		},
	)

	connectionForm := widget.NewForm(
		widget.NewFormItem("模式", mode),
		widget.NewFormItem("服务地址", baseURL),
		widget.NewFormItem("用户名", username),
		widget.NewFormItem("密码", password),
	)
	mode.SetSelected(s.config.Mode)
	s.updateLocalButtons()

	actions := container.NewHBox(connect, save, s.startButton, s.stopButton)
	top := container.NewBorder(nil, nil, nil, actions, connectionForm)
	scripts := container.NewBorder(widget.NewLabel("脚本"), container.NewHBox(refresh, s.runButton), nil, nil, s.scriptSelect)
	runInfo := container.NewBorder(status, nil, nil, nil, output)
	mainSplit := container.NewHSplit(scripts, runInfo)
	mainSplit.Offset = 0.25

	return container.NewBorder(top, container.NewVBox(widget.NewLabel("最近执行"), history), nil, nil, mainSplit)
}

func (s *guiState) connect() {
	s.connectWithRetry(1, 0, "连接中...")
}

func (s *guiState) connectWithRetry(attempts int, delay time.Duration, status string) {
	if attempts < 1 {
		attempts = 1
	}
	s.connectSeq++
	seq := s.connectSeq
	s.setStatus(status)
	client := apiclient.New(s.config.BaseURL, s.config.Username, s.config.Password)
	s.refreshSeq++

	go func() {
		var scripts []string
		var lastErr error
		for attempt := 1; attempt <= attempts; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := client.Health(ctx)
			if err == nil {
				scripts, err = client.Scripts(ctx)
			}
			cancel()
			if err == nil {
				lastErr = nil
				break
			}
			lastErr = err
			if attempt < attempts {
				time.Sleep(delay)
			}
		}

		fyne.Do(func() {
			if seq != s.connectSeq {
				return
			}
			if lastErr != nil {
				s.setStatus("连接失败: " + lastErr.Error())
				return
			}
			s.client = client
			s.applyScripts(scripts)
			s.setStatus(fmt.Sprintf("已连接，已加载 %d 个脚本", len(scripts)))
		})
	}()
}

func (s *guiState) refreshScripts() {
	client := s.client
	if client == nil {
		s.setStatus("请先连接服务")
		return
	}
	s.refreshSeq++
	seq := s.refreshSeq
	s.setStatus("刷新脚本中...")

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		scripts, err := client.Scripts(ctx)
		fyne.Do(func() {
			if seq != s.refreshSeq || client != s.client {
				return
			}
			if err != nil {
				s.setStatus("刷新脚本失败: " + err.Error())
				return
			}
			s.applyScripts(scripts)
			s.setStatus(fmt.Sprintf("已加载 %d 个脚本", len(scripts)))
		})
	}()
}

func (s *guiState) runSelectedScript() {
	client := s.client
	if client == nil {
		s.setStatus("请先连接服务")
		return
	}
	script := s.scriptSelect.Selected
	if script == "" {
		s.setStatus("请选择脚本")
		return
	}

	s.running = true
	s.updateRunButton()
	_ = s.outputText.Set("")
	s.setStatus("运行中: " + script)

	go func() {
		err := client.RunStream(context.Background(), script, func(event apiclient.StreamEvent) {
			fyne.Do(func() {
				s.handleEvent(event)
			})
		})
		if err != nil {
			fyne.Do(func() {
				s.appendOutput("[error] " + err.Error() + "\r\n")
				s.setStatus("请求失败: " + err.Error())
				s.addHistory(script + " 请求失败")
			})
		}
		fyne.Do(func() {
			s.running = false
			s.updateRunButton()
		})
	}()
}

func (s *guiState) applyScripts(scripts []string) {
	if s.scriptSelect == nil {
		return
	}
	selected := s.scriptSelect.Selected
	s.scriptSelect.Options = scripts
	if len(scripts) == 0 {
		s.scriptSelect.ClearSelected()
		s.scriptSelect.Refresh()
		s.updateRunButton()
		return
	}

	found := false
	for _, script := range scripts {
		if script == selected {
			found = true
			break
		}
	}
	if found {
		s.scriptSelect.SetSelected(selected)
	} else {
		s.scriptSelect.SetSelected(scripts[0])
	}
	s.scriptSelect.Refresh()
	s.updateRunButton()
}

func (s *guiState) clearScripts() {
	if s.scriptSelect == nil {
		return
	}
	s.scriptSelect.Options = nil
	s.scriptSelect.ClearSelected()
	s.scriptSelect.Refresh()
	s.updateRunButton()
}

func (s *guiState) updateRunButton() {
	if s.runButton == nil {
		return
	}
	if s.client != nil && !s.running && s.scriptSelect.Selected != "" {
		s.runButton.Enable()
		return
	}
	s.runButton.Disable()
}

func (s *guiState) updateLocalButtons() {
	if s.startButton == nil || s.stopButton == nil {
		return
	}
	if s.config.Mode == guiconfig.ModeLocal {
		s.startButton.Enable()
		s.stopButton.Enable()
		return
	}
	s.startButton.Disable()
	s.stopButton.Disable()
}

func (s *guiState) handleEvent(event apiclient.StreamEvent) {
	switch event.Type {
	case apiclient.EventOutput:
		prefix := "[stdout] "
		if event.Stream == "stderr" {
			prefix = "[stderr] "
		}
		s.appendOutput(prefix + event.Data)
	case apiclient.EventFinal:
		status := "成功"
		if event.TimedOut {
			status = "超时"
		} else if event.Error != "" {
			status = "请求失败"
		} else if event.ExitCode != 0 {
			status = "脚本失败"
		}
		s.setStatus(fmt.Sprintf("%s: %s exitCode=%d durationMs=%d", status, event.Script, event.ExitCode, event.DurationMs))
		s.addHistory(fmt.Sprintf("%s %s exitCode=%d durationMs=%d", event.Script, status, event.ExitCode, event.DurationMs))
	}
}

func (s *guiState) startLocalService() {
	if s.config.Mode != guiconfig.ModeLocal {
		s.setStatus("远程模式不管理本机服务")
		return
	}
	s.setStatus("启动服务中...")
	go func() {
		err := s.service.Start(context.Background())
		fyne.Do(func() {
			if err != nil {
				s.setStatus("启动服务失败: " + err.Error())
				return
			}
			s.connectWithRetry(20, 500*time.Millisecond, fmt.Sprintf("服务已启动 PID=%d，连接中...", s.service.PID()))
		})
	}()
}

func (s *guiState) stopLocalService() {
	if s.config.Mode != guiconfig.ModeLocal {
		s.setStatus("远程模式不管理本机服务")
		return
	}
	go func() {
		wasRunning := s.service.Running()
		err := s.service.Stop()
		fyne.Do(func() {
			if err != nil {
				s.setStatus("停止服务失败: " + err.Error())
				return
			}
			if wasRunning {
				s.client = nil
				s.connectSeq++
				s.refreshSeq++
				s.clearScripts()
			}
			s.setStatus("服务已停止")
		})
	}()
}

func (s *guiState) setStatus(value string) {
	_ = s.statusText.Set(value)
}

func (s *guiState) appendOutput(value string) {
	current, _ := s.outputText.Get()
	_ = s.outputText.Set(capOutput(current + value))
}

func (s *guiState) addHistory(value string) {
	items, _ := s.history.Get()
	items = append([]string{strings.TrimSpace(value)}, items...)
	if len(items) > 20 {
		items = items[:20]
	}
	_ = s.history.Set(items)
}
