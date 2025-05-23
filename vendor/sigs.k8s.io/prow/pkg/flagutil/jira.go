/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package flagutil

import (
	"errors"
	"flag"
	"fmt"
	"github.com/hashicorp/go-retryablehttp"
	"net/url"
	"time"

	"sigs.k8s.io/prow/pkg/config/secret"
	"sigs.k8s.io/prow/pkg/jira"
)

// jiraFlagParams struct is used indirectly by users of this package to customize
// the common Jira flags behavior, such as providing their own default values or
// suppressing the presence of certain flags
type jiraFlagParams struct {
	defaults         JiraOptions
	disableBasicAuth bool
}

// JiraFlagParameter is a functional option type for configuring Jira flags
type JiraFlagParameter func(*jiraFlagParams)

type JiraOptions struct {
	endpoint        string
	username        string
	passwordFile    string
	bearerTokenFile string
	backoff         retryablehttp.Backoff
	retryWaitMin    time.Duration
	retryWaitMax    time.Duration
	retryMax        int
}

// JiraNoBasicAuth disables the presence of the basic auth flags
func JiraNoBasicAuth() JiraFlagParameter {
	return func(params *jiraFlagParams) {
		params.disableBasicAuth = true
	}
}

// JiraDefaultEndpoint sets the default Jira endpoint
func JiraDefaultEndpoint(endpoint string) JiraFlagParameter {
	return func(params *jiraFlagParams) {
		params.defaults.endpoint = endpoint
	}
}

// JiraDefaultBearerTokenFile sets the default Jira bearer token file
func JiraDefaultBearerTokenFile(bearerTokenFile string) JiraFlagParameter {
	return func(params *jiraFlagParams) {
		params.defaults.bearerTokenFile = bearerTokenFile
	}
}

// AddCustomizedFlags injects Jira options into the given FlagSet. Behavior can be customized
// via functional options.
func (o *JiraOptions) AddCustomizedFlags(fs *flag.FlagSet, params ...JiraFlagParameter) {
	o.addFlags(fs, params...)
}

// AddFlags injects Jira options into the given FlagSet
func (o *JiraOptions) AddFlags(fs *flag.FlagSet) {
	o.addFlags(fs)
}

func (o *JiraOptions) addFlags(fs *flag.FlagSet, paramFuncs ...JiraFlagParameter) {
	params := jiraFlagParams{
		defaults:         JiraOptions{},
		disableBasicAuth: false,
	}

	for _, parametrize := range paramFuncs {
		parametrize(&params)
	}

	fs.StringVar(&o.endpoint, "jira-endpoint", params.defaults.endpoint, "The Jira endpoint to use")
	fs.StringVar(&o.bearerTokenFile, "jira-bearer-token-file", params.defaults.bearerTokenFile, "Location to a file containing the Jira bearer authorization token")

	if !params.disableBasicAuth {
		fs.StringVar(&o.username, "jira-username", params.defaults.username, "The username to use for Jira basic auth")
		fs.StringVar(&o.passwordFile, "jira-password-file", params.defaults.passwordFile, "Location to a file containing the Jira basic auth password")
	}
}

func (o *JiraOptions) Validate(_ bool) error {
	if o.endpoint == "" {
		return nil
	}

	if _, err := url.ParseRequestURI(o.endpoint); err != nil {
		return fmt.Errorf("--jira-endpoint %q is invalid: %w", o.endpoint, err)
	}

	if (o.username != "") != (o.passwordFile != "") {
		return errors.New("--jira-username and --jira-password-file must be specified together")
	}

	if o.bearerTokenFile != "" && o.username != "" {
		return errors.New("--jira-bearer-token-file and --jira-username are mutually exclusive")
	}

	if o.bearerTokenFile != "" && o.passwordFile != "" {
		return errors.New("--jira-bearer-token-file and --jira-password-file are mutually exclusive")
	}

	return nil
}

func (o *JiraOptions) CustomBackoff(backoff retryablehttp.Backoff) {
	o.backoff = backoff
}

func (o *JiraOptions) CustomRetryWaitMin(retryWaitMin time.Duration) {
	o.retryWaitMin = retryWaitMin
}

func (o *JiraOptions) CustomRetryWaitMax(retryWaitMax time.Duration) {
	o.retryWaitMax = retryWaitMax
}

func (o *JiraOptions) CustomRetryMax(retryMax int) {
	o.retryMax = retryMax
}

func (o *JiraOptions) Client() (jira.Client, error) {
	if o.endpoint == "" {
		return nil, errors.New("empty --jira-endpoint, can not create a client")
	}

	var opts []jira.Option
	if o.passwordFile != "" {
		if err := secret.Add(o.passwordFile); err != nil {
			return nil, fmt.Errorf("failed to get --jira-password-file: %w", err)
		}
		opts = append(opts, jira.WithBasicAuth(func() (string, string) {
			return o.username, string(secret.GetSecret(o.passwordFile))
		}))
	}

	if o.bearerTokenFile != "" {
		if err := secret.Add(o.bearerTokenFile); err != nil {
			return nil, fmt.Errorf("failed to get --jira-bearer-token-file: %w", err)
		}
		opts = append(opts, jira.WithBearerAuth(func() string {
			return string(secret.GetSecret(o.bearerTokenFile))
		}))
	}

	if o.backoff != nil {
		opts = append(opts, jira.WithBackOff(o.backoff))
	}
	if o.retryWaitMin != 1*time.Second {
		opts = append(opts, jira.WithRetryWaitMin(o.retryWaitMin))
	}
	if o.retryWaitMax != 30*time.Second {
		opts = append(opts, jira.WithRetryWaitMax(o.retryWaitMax))
	}
	if o.retryMax != 4 {
		opts = append(opts, jira.WithRetryMax(o.retryMax))
	}

	return jira.NewClient(o.endpoint, opts...)
}
