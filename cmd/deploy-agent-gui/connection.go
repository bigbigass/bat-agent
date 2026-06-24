package main

import (
	"fmt"

	"github.com/liqixin/deploy-agent/internal/appservice"
	"github.com/liqixin/deploy-agent/internal/gui/guiconfig"
)

type clientConfig struct {
	BaseURL  string
	Username string
	Password string
}

type localHTTPProvider interface {
	HTTPClientConfig() (appservice.HTTPClientConfig, bool)
}

func clientConfigForMode(mode string, cfg guiconfig.Config, local localHTTPProvider) (clientConfig, error) {
	if mode == guiconfig.ModeLocal {
		if local == nil {
			return clientConfig{}, fmt.Errorf("本机服务 HTTP 未启用")
		}
		localCfg, ok := local.HTTPClientConfig()
		if !ok {
			return clientConfig{}, fmt.Errorf("本机服务 HTTP 未启用，请在 config.yaml 中启用 services.http.enabled")
		}
		return clientConfig{
			BaseURL:  localCfg.BaseURL,
			Username: localCfg.Username,
			Password: localCfg.Password,
		}, nil
	}
	return clientConfig{
		BaseURL:  cfg.BaseURL,
		Username: cfg.Username,
		Password: cfg.Password,
	}, nil
}
