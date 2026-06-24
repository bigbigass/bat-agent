//go:build cgo

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/widget"
	"github.com/liqixin/deploy-agent/internal/appservice"
	"github.com/liqixin/deploy-agent/internal/gui/apiclient"
	"github.com/liqixin/deploy-agent/internal/gui/guiconfig"
)

type guiState struct {
	configPath   string
	config       guiconfig.Config
	client       *apiclient.Client
	service      *appservice.Service
	remoteFields fyne.CanvasObject

	statusText binding.String
	outputText binding.String
	history    binding.StringList

	scriptSelect *widget.Select
	runButton    *widget.Button

	preDownloadCheck *widget.Check
	projectEntry     *widget.Entry
	artifactEntry    *widget.Entry

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

	state := &guiState{
		configPath: cfgPath,
		config:     cfg,
		service:    appservice.New(appservice.Options{}),
		statusText: binding.NewString(),
		outputText: binding.NewString(),
		history:    binding.NewStringList(),
	}
	state.setStatus("服务启动中...")

	w.SetContent(state.buildUI())
	state.startEmbeddedService()
	w.ShowAndRun()
}

func (s *guiState) buildUI() fyne.CanvasObject {
	mode := widget.NewSelect([]string{guiconfig.ModeLocal, guiconfig.ModeRemote}, func(value string) {
		s.config.Mode = value
		s.updateModeControls()
		s.updateRunButton()
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
		if err := guiconfig.Save(s.configPath, guiconfig.ForSave(s.config)); err != nil {
			s.setStatus("保存配置失败: " + err.Error())
			return
		}
		s.setStatus("配置已保存")
	})

	s.preDownloadCheck = widget.NewCheck("执行前下载", func(bool) {
		s.updatePreDownloadInputs()
		s.updateRunButton()
	})
	s.projectEntry = widget.NewEntry()
	s.projectEntry.SetPlaceHolder("项目编号")
	s.projectEntry.OnChanged = func(string) {
		s.updateRunButton()
	}
	s.artifactEntry = widget.NewEntry()
	s.artifactEntry.SetPlaceHolder("产物文件名")
	s.artifactEntry.OnChanged = func(string) {
		s.updateRunButton()
	}
	s.updatePreDownloadInputs()

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

	modeForm := widget.NewForm(widget.NewFormItem("模式", mode))
	s.remoteFields = widget.NewForm(
		widget.NewFormItem("服务地址", baseURL),
		widget.NewFormItem("用户名", username),
		widget.NewFormItem("密码", password),
	)
	mode.SetSelected(s.config.Mode)
	s.updateModeControls()

	actions := container.NewHBox(connect, save)
	top := container.NewBorder(nil, nil, nil, actions, container.NewVBox(modeForm, s.remoteFields))
	runForm := widget.NewForm(
		widget.NewFormItem("", s.preDownloadCheck),
		widget.NewFormItem("项目编号", s.projectEntry),
		widget.NewFormItem("产物文件名", s.artifactEntry),
	)
	scripts := container.NewBorder(widget.NewLabel("脚本"), container.NewHBox(refresh, s.runButton), nil, nil, container.NewVBox(s.scriptSelect, runForm))
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
	clientCfg, err := clientConfigForMode(s.config.Mode, s.config, s.service)
	if err != nil {
		s.setStatus(err.Error())
		return
	}
	client := apiclient.New(clientCfg.BaseURL, clientCfg.Username, clientCfg.Password)
	s.client = nil
	s.refreshSeq++
	s.updateRunButton()

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
	opts, err := preDownloadOptions(s.preDownloadCheck != nil && s.preDownloadCheck.Checked, entryText(s.projectEntry), entryText(s.artifactEntry))
	if err != nil {
		s.setStatus(err.Error())
		return
	}

	s.running = true
	s.updateRunButton()
	_ = s.outputText.Set("")
	s.setStatus("运行中: " + script)

	go func() {
		err := client.RunStreamWithOptions(context.Background(), script, opts, func(event apiclient.StreamEvent) {
			fyne.Do(func() {
				s.handleEvent(event)
			})
		})
		fyne.Do(func() {
			refreshAfterRun := false
			if err != nil {
				status, refresh := runErrorStatus(err)
				refreshAfterRun = refresh && client == s.client
				s.appendOutput("[error] " + err.Error() + "\r\n")
				s.setStatus(status)
				s.addHistory(script + " 请求失败")
			}
			s.running = false
			s.updateRunButton()
			if refreshAfterRun {
				s.refreshScripts()
			}
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
	ready := true
	if s.preDownloadCheck != nil && s.projectEntry != nil && s.artifactEntry != nil {
		ready = preDownloadInputsReady(s.preDownloadCheck.Checked, s.projectEntry.Text, s.artifactEntry.Text)
	}
	if s.client != nil && !s.running && s.scriptSelect != nil && s.scriptSelect.Selected != "" && ready {
		s.runButton.Enable()
		return
	}
	s.runButton.Disable()
}

func (s *guiState) updatePreDownloadInputs() {
	if s.preDownloadCheck == nil || s.projectEntry == nil || s.artifactEntry == nil {
		return
	}
	if s.preDownloadCheck.Checked {
		s.projectEntry.Enable()
		s.artifactEntry.Enable()
		return
	}
	s.projectEntry.Disable()
	s.artifactEntry.Disable()
}

func (s *guiState) startEmbeddedService() {
	go func() {
		err := s.service.Start(context.Background())
		fyne.Do(func() {
			if err != nil {
				s.setStatus("服务启动失败: " + err.Error())
				s.updateRunButton()
				return
			}
			if s.config.Mode == guiconfig.ModeLocal {
				s.connectWithRetry(20, 500*time.Millisecond, "服务已启动，连接中...")
				return
			}
			s.setStatus("服务已启动")
		})
	}()
}

func (s *guiState) updateModeControls() {
	if s.remoteFields == nil {
		return
	}
	if s.config.Mode == guiconfig.ModeLocal {
		s.remoteFields.Hide()
		return
	}
	s.remoteFields.Show()
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

func entryText(entry *widget.Entry) string {
	if entry == nil {
		return ""
	}
	return entry.Text
}
