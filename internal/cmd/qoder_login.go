package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoQoderLogin starts Qoder (China mainland) browser authorization and saves the tokens.
func DoQoderLogin(cfg *config.Config, options *LoginOptions) {
	doQoderLogin(cfg, options, qoder.RealmCN, "Qoder")
}

// DoQoderINTLLogin starts Qoder (international) browser authorization and saves the tokens.
func DoQoderINTLLogin(cfg *config.Config, options *LoginOptions) {
	doQoderLogin(cfg, options, qoder.RealmINTL, "Qoder (International)")
}

func doQoderLogin(cfg *config.Config, options *LoginOptions, realm qoder.RealmConfig, displayName string) {
	if options == nil {
		options = &LoginOptions{}
	}
	authOpts := &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    options.Prompt,
	}
	if personalToken := options.PersonalToken; personalToken != "" {
		authOpts.Metadata["personal_token"] = personalToken
	}
	record, savedPath, err := newAuthManager().Login(context.Background(), realm.Provider, cfg, authOpts)
	if err != nil {
		log.Errorf("%s authentication failed: %v", displayName, err)
		return
	}
	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Printf("%s authentication successful!\n", displayName)
}
