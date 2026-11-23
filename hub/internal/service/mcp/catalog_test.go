package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"

	agents "arkade/hub/api/agents/v1"
	external "arkade/hub/api/external/v1"
	images "arkade/hub/api/imagetasks/v1"
	resources "arkade/hub/api/resources/v1"
	stacks "arkade/hub/api/stacks/v1"
	"arkade/protocol/composehash"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestCatalogContracts(t *testing.T) {
	apiRoutes := []any{
		agents.ListReq{},
		stacks.ListReq{}, stacks.DetailReq{}, stacks.ConfigReq{}, stacks.PSReq{},
		stacks.CreateReq{}, stacks.UpdateReq{}, stacks.DeleteReq{}, stacks.StartOperationReq{}, stacks.GetOperationReq{}, stacks.MigrateReq{},
		external.DetailReq{}, external.ConfigReq{}, external.DraftReq{}, external.RetryAdoptReq{}, external.ImportPreviewReq{}, external.ImportReq{}, external.ApplyPreviewReq{}, external.ApplyReq{}, external.CopyReq{},
		resources.ListContainersReq{}, resources.ContainerDetailReq{}, resources.ContainerLogsReq{}, resources.ContainerMetricsReq{},
		resources.ContainerStartReq{}, resources.ContainerStopReq{}, resources.ContainerRestartReq{}, resources.ContainerRemoveReq{},
		resources.ListNetworksReq{}, resources.RemoveNetworkReq{}, resources.ListVolumesReq{}, resources.RemoveVolumeReq{}, resources.VolumeFilesReq{},
		resources.ListImagesReq{}, resources.RemoveImageReq{}, images.PullReq{}, images.ExportReq{}, images.MigrateReq{}, images.ListReq{}, images.GetReq{}, images.CancelReq{},
	}
	routes := make(map[string]reflect.Type, len(apiRoutes))
	for _, api := range apiRoutes {
		typ := reflect.TypeOf(api)
		meta, ok := typ.FieldByName("Meta")
		if !ok || meta.Tag.Get("raw") == "true" {
			t.Fatalf("not a native JSON API request: %s", typ)
		}
		routes[strings.ToUpper(meta.Tag.Get("method"))+" "+meta.Tag.Get("path")] = typ
	}
	seenNames := map[string]bool{}
	seenRoutes := map[string]bool{}
	nameRE := regexp.MustCompile(`^arkade_[a-z0-9_]+$`)
	pathParamRE := regexp.MustCompile(`\{([^{}]+)\}`)
	for _, tool := range Catalog() {
		if !nameRE.MatchString(tool.Name) || seenNames[tool.Name] {
			t.Fatalf("invalid or duplicate tool name %q", tool.Name)
		}
		seenNames[tool.Name] = true
		t.Run(tool.Name, func(t *testing.T) {
			key := tool.Method + " " + tool.Path
			api := routes[key]
			if api == nil {
				t.Fatalf("route %s is not an allowed JSON endpoint", key)
			}
			seenRoutes[key] = true
			encoded, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema jsonschema.Schema
			if err := json.Unmarshal(encoded, &schema); err != nil {
				t.Fatal(err)
			}
			if _, err := schema.Resolve(nil); err != nil {
				t.Fatal(err)
			}
			if tool.InputSchema["additionalProperties"] != false || schema.Type != "object" || (tool.ReadOnly && tool.Destructive) {
				t.Fatal("invalid schema or risk flags")
			}
			transport := map[string]bool{}
			for _, match := range pathParamRE.FindAllStringSubmatch(tool.Path, -1) {
				name := match[1]
				p := schema.Properties[name]
				if p == nil || p.Type != "string" || !containsCatalogString(schema.Required, name) {
					t.Fatalf("path parameter %q must be a required string", name)
				}
				transport[name] = true
			}
			for _, query := range tool.Query {
				if transport[query] || schema.Properties[query] == nil || !catalogAPIHasQuery(api, query) {
					t.Fatalf("invalid query parameter %q", query)
				}
				transport[query] = true
			}
			if tool.Method == http.MethodGet || tool.Method == http.MethodDelete {
				if len(tool.FixedBody) != 0 || len(transport) != len(schema.Properties) {
					t.Fatal("GET or DELETE has body fields")
				}
			}
			for fixed := range tool.FixedBody {
				if schema.Properties[fixed] != nil || containsCatalogString(schema.Required, fixed) {
					t.Fatalf("fixed body field %q is exposed to the caller", fixed)
				}
			}
		})
	}
	for route := range routes {
		if !seenRoutes[route] {
			t.Errorf("missing tool for %s", route)
		}
	}
}

// TestCatalogConfigHashMatchesProjectHash 验证 MCP schema 里哈希字段的长度
// 约束与 composehash.Project 的真实输出一致。
//
// 这是回归用例：曾经 schema 声明 64 位而实际哈希是 16 位，客户端传 16 位
// 违反 pattern、传 64 位又和 Agent 算出的值对不上，导致所有需要回传
// config_hash 的 MCP 工具（import_stack、adopt_external_stack 等）不可用。
// 直接在测试里比较两边，哈希长度一旦变化就会立刻失败。
func TestCatalogConfigHashMatchesProjectHash(t *testing.T) {
	wantLen := len(composehash.Project("services:\n  app:\n    image: alpine\n", ""))
	if wantLen != 16 {
		t.Fatalf("项目哈希长度变成 %d，需同步更新 MCP schema、前端约定与文档", wantLen)
	}

	hashFields := map[string]bool{
		"expected_content_hash": true,
		"expected_config_hash":  true,
		"source_hash":           true,
	}
	wantPattern := fmt.Sprintf("^[a-f0-9]{%d}$", wantLen)
	checked := 0
	for _, tool := range Catalog() {
		props, ok := tool.InputSchema["properties"].(map[string]any)
		if !ok {
			continue
		}
		for name, raw := range props {
			if !hashFields[name] {
				continue
			}
			checked++
			schema, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("%s/%s: schema 不是对象", tool.Name, name)
			}
			if schema["pattern"] != wantPattern {
				t.Errorf("%s/%s: pattern=%v，应为 %s", tool.Name, name, schema["pattern"], wantPattern)
			}
			if schema["minLength"] != wantLen || schema["maxLength"] != wantLen {
				t.Errorf("%s/%s: minLength=%v maxLength=%v，应均为 %d",
					tool.Name, name, schema["minLength"], schema["maxLength"], wantLen)
			}
		}
	}
	if checked == 0 {
		t.Fatal("未找到任何哈希字段，测试已与 Catalog 脱节")
	}
}

func catalogAPIHasQuery(api reflect.Type, name string) bool {
	for _, f := range reflect.VisibleFields(api) {
		if f.Tag.Get("in") != "query" {
			continue
		}
		key := f.Tag.Get("name")
		if key == "" {
			key, _, _ = strings.Cut(f.Tag.Get("json"), ",")
		}
		if name == key {
			return true
		}
	}
	return false
}

func containsCatalogString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
