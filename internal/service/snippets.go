// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/ZeraOrg/opentidemark/internal/meta"
)

// KeyPlaceholder stands in for the connection key, which is never shown again after creation.
const KeyPlaceholder = "<YOUR_TIDEMARK_KEY>"

// Snippet is a ready-to-paste client configuration.
type Snippet struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Language string `json:"language"`
	Content  string `json:"content"`
}

// nativeIngestPath is the native JSON endpoint (SPEC §9.5) that Logstash's http output posts to.
const nativeIngestPath = "/api/v1/ingest/logs"

// Snippets renders client configurations for a connection using the public URL (SPEC §11.5).
// Seq clients use the base URL, Elasticsearch shippers <base>/es, OTLP <base>/otlp and Logstash
// the native endpoint through its http output (SPEC §9.3).
func Snippets(c meta.Connection, public *url.URL) []Snippet {
	base := strings.TrimSuffix(public.String(), "/")
	host := public.Hostname()
	port := public.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[public.Scheme]
	}
	ssl := public.Scheme == "https"
	return []Snippet{
		{"serilog", "Serilog (Serilog.Sinks.Seq)", "csharp",
			fmt.Sprintf("Log.Logger = new LoggerConfiguration()\n    .WriteTo.Seq(%q, apiKey: %q)\n    .CreateLogger();", base, KeyPlaceholder)},
		{"nlog", "NLog (NLog.Targets.Seq)", "xml",
			fmt.Sprintf("<target xsi:type=\"Seq\" name=\"tidemark\" serverUrl=%q apiKey=%q />", base, KeyPlaceholder)},
		{"otel", "OpenTelemetry SDK (environment variables)", "shell",
			fmt.Sprintf("OTEL_EXPORTER_OTLP_ENDPOINT=%s/otlp\nOTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf\nOTEL_EXPORTER_OTLP_HEADERS=x-tidemark-key=%s", base, KeyPlaceholder)},
		{"filebeat", "Filebeat", "yaml",
			fmt.Sprintf("output.elasticsearch:\n  hosts: [%q]\n  path: \"/es\"\n  api_key: %q\n  index: %q\nsetup.ilm.enabled: false\nsetup.template.enabled: false",
				base, "tidemark:"+KeyPlaceholder, c.DefaultStream)},
		{"fluentbit", "Fluent Bit", "ini",
			fmt.Sprintf("[OUTPUT]\n    Name               es\n    Match              *\n    Host               %s\n    Port               %s\n    Path               /es\n    HTTP_User          tidemark\n    HTTP_Passwd        %s\n    Index              %s\n    Suppress_Type_Name On\n    tls                %s",
				host, port, KeyPlaceholder, c.DefaultStream, onOff(ssl))},
		{"vector", "Vector", "toml",
			fmt.Sprintf("[sinks.tidemark]\ntype = \"elasticsearch\"\ninputs = [\"*\"]\nendpoints = [%q]\napi_version = \"v8\"\nbulk.index = %q\nauth.strategy = \"basic\"\nauth.user = \"tidemark\"\nauth.password = %q",
				base+"/es", c.DefaultStream, KeyPlaceholder)},
		{"logstash", "Logstash", "ruby",
			fmt.Sprintf("output {\n  http {\n    url => %q\n    http_method => \"post\"\n    format => \"json_batch\"\n    headers => { \"X-Tidemark-Key\" => %q }\n  }\n}",
				base+nativeIngestPath, KeyPlaceholder)},
	}
}

func onOff(b bool) string {
	if b {
		return "On"
	}
	return "Off"
}
