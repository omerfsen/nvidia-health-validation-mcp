// Copyright (c) 2026, Omer Sen.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/omerfsen/nvidia-health-validation-mcp/pkg/hvclient"
)

const (
	serverJSONPath = "../../server.json"
	dockerfilePath = "../../Dockerfile"
	readmePath     = "../../README.md"
)

var settings = []string{
	hvclient.EnvHost, hvclient.EnvPort, hvclient.EnvOwnerUID, hvclient.EnvEvaluateTimeout,
	EnvAllowEvaluate, EnvEvaluateInterval,
}

type serverJSON struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Packages []struct {
		Identifier       string `json:"identifier"`
		RuntimeArguments []struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"runtimeArguments"`
		EnvironmentVariables []struct {
			Name string `json:"name"`
		} `json:"environmentVariables"`
	} `json:"packages"`
}

func readServerJSON(t *testing.T) serverJSON {
	t.Helper()

	raw, err := os.ReadFile(serverJSONPath)
	require.NoError(t, err)

	var parsed serverJSON
	require.NoError(t, json.Unmarshal(raw, &parsed))

	return parsed
}

// server.json goes stale silently, so it must name exactly the settings the code reads.
func TestServerJSONNamesEverySetting(t *testing.T) {
	parsed := readServerJSON(t)
	require.Len(t, parsed.Packages, 1)

	var named []string
	for _, variable := range parsed.Packages[0].EnvironmentVariables {
		named = append(named, variable.Name)
	}

	assert.ElementsMatch(t, settings, named)
	assert.True(t, strings.HasSuffix(parsed.Packages[0].Identifier, ":"+parsed.Version),
		"the image tag must be the server.json version")
}

// serve listens on loopback, so a client installing from the Registry must run the
// image in serve's network namespace, hardened as the README's docker run line is.
func TestServerJSONRunsTheImageLikeTheReadme(t *testing.T) {
	parsed := readServerJSON(t)
	require.Len(t, parsed.Packages, 1)

	var flags []string

	for _, argument := range parsed.Packages[0].RuntimeArguments {
		assert.Equal(t, "named", argument.Type)
		flags = append(flags, argument.Name+"="+argument.Value)
	}

	assert.Contains(t, flags, "--network=host")

	raw, err := os.ReadFile(readmePath)
	require.NoError(t, err)

	run := regexp.MustCompile(`(?s)docker run --rm -i (.*?)\n  -e `).FindSubmatch(raw)
	require.NotNil(t, run, "README.md has no docker run line")

	readme := strings.Fields(strings.ReplaceAll(string(run[1]), "\\", ""))
	for i, flag := range readme {
		if !strings.Contains(flag, "=") {
			readme[i] = flag + "=true"
		}
	}

	assert.ElementsMatch(t, readme, flags)
}

// The Registry matches the image to server.json by this label.
func TestTheImageLabelMatchesServerJSON(t *testing.T) {
	raw, err := os.ReadFile(dockerfilePath)
	require.NoError(t, err)

	assert.Contains(t, string(raw), `LABEL io.modelcontextprotocol.server.name="`+readServerJSON(t).Name+`"`)
}

func TestTheReadmeListsEveryToolAndSetting(t *testing.T) {
	raw, err := os.ReadFile(readmePath)
	require.NoError(t, err)

	readme := string(raw)

	for _, name := range append(toolNames(t, start(t, evaluateOn())), settings...) {
		assert.Contains(t, readme, "`"+name+"`", "README.md does not mention %s", name)
	}
}
