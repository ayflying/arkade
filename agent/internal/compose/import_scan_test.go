package compose

// import_scan_test.go 覆盖导入前的安全扫描，重点是**绕过方式**。
//
// 这组测试的存在理由：早先版本按行做字符串匹配，被指出可以被
// flow style、锚点/merge、自定义标签绕过。现在改为真正的 yaml.v3 节点树，
// 下面的用例就是那些绕过手法的"回归钉子"——每一条都必须被拒。

import (
	"strings"
	"testing"
)

// 判定辅助：结果里是否提到某个字段名。
func mentions(blockers []ImportBlocker, needle string) bool {
	for _, b := range blockers {
		if strings.Contains(b.String(), needle) {
			return true
		}
	}
	return false
}

// TestScanAllowsPlainCompose 验证普通编排不被误拒。
//
// 误拒的代价同样真实：用户明明可以导入却被挡住，功能就等于不存在。
func TestScanAllowsPlainCompose(t *testing.T) {
	cases := []string{
		"",
		"services:\n  web:\n    image: nginx:1.25\n    ports:\n      - \"8080:80\"\n",
		// 命名卷不是路径，不能误判成相对挂载。
		"services:\n  web:\n    image: nginx\n    volumes:\n      - data:/var/lib/data\n",
		// 绝对路径挂载是安全的（导入后仍然指向同一位置）。
		"services:\n  web:\n    image: nginx\n    volumes:\n      - /etc/localtime:/etc/localtime:ro\n",
		// 只有环境变量插值、没有文件依赖。
		"services:\n  db:\n    image: postgres\n    environment:\n      POSTGRES_PASSWORD: ${DB_PW}\n",
	}
	for _, c := range cases {
		if got := ScanImportSafety(c); len(got) != 0 {
			t.Errorf("不应拒绝: %q\n阻塞项: %v", c, got)
		}
	}
}

// TestScanRejectsRelativeEnvFile 验证相对 env_file 被拒。
//
// 用户真机验收用例（Lead 指定）：这是最常见的"导入后行为就变了"的写法。
// 注意**不要求 app.env 真实存在**——我们拒绝的是相对引用本身。
func TestScanRejectsRelativeEnvFile(t *testing.T) {
	content := "services:\n  app:\n    image: alpine:3.19\n    env_file: ./app.env\n"
	got := ScanImportSafety(content)
	if len(got) == 0 {
		t.Fatal("相对 env_file 应被拒绝")
	}
	if !mentions(got, "env_file") {
		t.Errorf("阻塞原因应指出 env_file，实际 %v", got)
	}
	if !mentions(got, "第 4 行") {
		t.Errorf("应给出行号便于定位，实际 %v", got)
	}
}

// TestScanRejectsAbsoluteEnvFile 验证绝对路径 env_file 也被拒。
//
// 绝对路径在导入后**确实**仍指向同一文件，看起来"安全"。
// 但它意味着导入的编排依赖一个我们不会复制、也无法确认存在的宿主文件：
// 一旦那台机器上文件被删或权限变化，编排就起不来，而界面上显示"已导入"。
// 首版明确拒绝，胜过给一个脆弱的"成功"。
func TestScanRejectsAbsoluteEnvFile(t *testing.T) {
	content := "services:\n  app:\n    image: alpine\n    env_file: /srv/app/.env\n"
	if got := ScanImportSafety(content); len(got) == 0 {
		t.Error("env_file 应一律拒绝（避免依赖未随导入复制的外部文件）")
	}
}

// TestScanRejectsFlowStyleBypass 验证 flow style 写法不能绕过。
//
// 这是旧版按行扫描最典型的漏洞：一行就能藏住整个危险字段，
// 按行匹配看到的是 `services: {web: {env_file: ./x}}` 这样一整行，
// 很难可靠地识别出 env_file。
func TestScanRejectsFlowStyleBypass(t *testing.T) {
	cases := []string{
		"services: {web: {image: nginx, env_file: ./x}}\n",
		"services: {web: {image: nginx, volumes: [\"./data:/data\"]}}\n",
		"services: {web: {image: nginx, build: .}}\n",
		"{services: {web: {env_file: ./.env}}}\n",
	}
	for _, c := range cases {
		if got := ScanImportSafety(c); len(got) == 0 {
			t.Errorf("flow style 写法应被拒绝: %q", c)
		}
	}
}

// TestScanRejectsAnchors 验证锚点/别名被明确拒绝。
//
// 锚点可以把危险字段藏在别处再引用进来；不展开就无法确定真实配置，
// 展开又可能被用来构造解析炸弹。首版明确拒绝，文案说明"暂不支持"。
func TestScanRejectsAnchors(t *testing.T) {
	content := `services:
  base: &base
    image: nginx
  web:
    <<: *base
    ports: ["80:80"]
`
	got := ScanImportSafety(content)
	if len(got) == 0 {
		t.Fatal("锚点/合并键应被拒绝")
	}
	if !mentions(got, "锚点") && !mentions(got, "合并键") {
		t.Errorf("文案应说明锚点或合并键，实际 %v", got)
	}
}

// TestScanRejectsMergeKeyAlone 验证单独出现 merge 键也被拒。
func TestScanRejectsMergeKeyAlone(t *testing.T) {
	content := "x-common: &c\n  image: nginx\nservices:\n  web:\n    <<: *c\n"
	if got := ScanImportSafety(content); len(got) == 0 {
		t.Error("merge 键应被拒绝")
	}
}

// TestScanAllowsExtensionFields 验证 x-* 扩展字段本身不被误拒。
//
// x-* 是 compose 官方允许的扩展字段，很多团队用它做片段复用。
// 只要不用锚点引用（上面的用例覆盖），仅仅"存在 x-*"不该阻止导入。
func TestScanAllowsExtensionFields(t *testing.T) {
	content := "x-meta:\n  owner: team-a\nservices:\n  web:\n    image: nginx\n"
	if got := ScanImportSafety(content); len(got) != 0 {
		t.Errorf("x-* 扩展字段不应被拒绝，实际 %v", got)
	}
}

// TestScanRejectsCustomTags 验证自定义标签被拒。
//
// !include、!env 这类自定义标签可能改变值的含义，
// 我们不展开就无法保证导入后一致。
func TestScanRejectsCustomTags(t *testing.T) {
	content := "services:\n  web:\n    image: !env IMAGE_TAG\n"
	got := ScanImportSafety(content)
	if len(got) == 0 {
		t.Fatal("自定义标签应被拒绝")
	}
	if !mentions(got, "标签") {
		t.Errorf("文案应说明标签问题，实际 %v", got)
	}
}

// TestScanRejectsBuild 验证 build 一律被拒。
//
// 构建上下文通常是相对目录，导入后路径基准变化，构建结果无法保证。
func TestScanRejectsBuild(t *testing.T) {
	for _, c := range []string{
		"services:\n  web:\n    build: .\n",
		"services:\n  web:\n    build:\n      context: ./app\n      dockerfile: Dockerfile\n",
	} {
		got := ScanImportSafety(c)
		if len(got) == 0 {
			t.Errorf("build 应被拒绝: %q", c)
		}
		if !mentions(got, "build") {
			t.Errorf("文案应指出 build，实际 %v", got)
		}
	}
}

// TestScanRejectsRelativeBindLongSyntax 验证长语法相对挂载被拒。
func TestScanRejectsRelativeBindLongSyntax(t *testing.T) {
	content := `services:
  web:
    image: nginx
    volumes:
      - type: bind
        source: ./conf
        target: /etc/nginx/conf.d
`
	got := ScanImportSafety(content)
	if len(got) == 0 {
		t.Fatal("长语法相对挂载应被拒绝")
	}
	if !mentions(got, "volumes") {
		t.Errorf("文案应指出 volumes，实际 %v", got)
	}
}

// TestScanRejectsIncludeAndTopLevelSecrets 验证 include 与顶层 secrets 的 file。
func TestScanRejectsIncludeAndTopLevelSecrets(t *testing.T) {
	if got := ScanImportSafety("include:\n  - other.yml\n"); len(got) == 0 {
		t.Error("include 应被拒绝")
	}
	content := "services:\n  db:\n    image: postgres\nsecrets:\n  dbcert:\n    file: ./certs/db.crt\n"
	got := ScanImportSafety(content)
	if len(got) == 0 {
		t.Fatal("顶层 secrets 的 file 应被拒绝")
	}
	if !mentions(got, "secrets.file") {
		t.Errorf("文案应指出 secrets.file，实际 %v", got)
	}
}

// TestScanRejectsUnparsableYAML 验证无法解析时拒绝而不是放行。
//
// 放行一份读不懂的文件，等于把"能不能正确运行"交给了运气。
func TestScanRejectsUnparsableYAML(t *testing.T) {
	for _, c := range []string{
		"services: [\n",
		"services:\n\tweb:\n", // tab 缩进
		"a: 1\n  b: 2\n",      // 缩进错乱
	} {
		if got := ScanImportSafety(c); len(got) == 0 {
			t.Errorf("无法解析的内容应被拒绝: %q", c)
		}
	}
}

// TestScanRejectsInterpolatedMountSource 验证含插值的挂载源被拒。
//
// 这是 Lead 指出的绕过：`- ${DATA}:/data` 的源字面上不是相对路径，
// 但展开后完全可能是 ./data。它展开成什么取决于外部项目当时的 .env
// 与宿主 shell 环境，而这两者我们都无法准确复现——
// 因此只要含插值就拒绝，不试图展开验证。
//
// 代价（已确认接受）：`${DATA}` 恰好是绝对路径的**合法**用法也会被拒。
// 首批宁可少导入几个，也不要导入一个数据挂错位置的项目。
func TestScanRejectsInterpolatedMountSource(t *testing.T) {
	cases := []string{
		// 短语法，花括号形式
		"services:\n  web:\n    image: nginx\n    volumes:\n      - ${DATA}:/data\n",
		// 短语法，无花括号形式
		"services:\n  web:\n    image: nginx\n    volumes:\n      - $PWD/x:/x\n",
		// 带默认值
		"services:\n  web:\n    image: nginx\n    volumes:\n      - ${DATA:-./fallback}:/data\n",
		// 长语法
		"services:\n  web:\n    image: nginx\n    volumes:\n      - type: bind\n        source: ${CONF}\n        target: /etc/app\n",
		// 命令替换（反引号）
		"services:\n  web:\n    image: nginx\n    volumes:\n      - \"`pwd`/x:/x\"\n",
	}
	for _, c := range cases {
		got := ScanImportSafety(c)
		if len(got) == 0 {
			t.Errorf("含插值的挂载源应被拒绝: %q", c)
		}
		if !mentions(got, "volumes") {
			t.Errorf("文案应指出 volumes，实际 %v", got)
		}
	}
}

// TestScanAllowsNamedVolumeMount 验证纯命名卷挂载仍被允许。
//
// 命名卷（`data:/x`）导入后语义不变，不会因基准目录改变而失效。
// 若把它也拒了，几乎所有真实编排都无法导入。
func TestScanAllowsNamedVolumeMount(t *testing.T) {
	content := "services:\n  db:\n    image: postgres\n    volumes:\n      - dbdata:/var/lib/postgresql/data\n"
	if got := ScanImportSafety(content); len(got) != 0 {
		t.Errorf("命名卷不应被拒绝，实际 %v", got)
	}
}

// TestScanBindLongSyntaxRequiresAbsolute 验证长语法 bind 的源必须是绝对路径。
//
// compose 语义里 bind 的 source 必须是绝对路径；`source: data` 不是命名卷，
// 而是相对路径。它导入后必然指向错误位置，因此必须拒绝。
func TestScanBindLongSyntaxRequiresAbsolute(t *testing.T) {
	bad := `services:
  web:
    image: nginx
    volumes:
      - type: bind
        source: data
        target: /data
`
	if got := ScanImportSafety(bad); len(got) == 0 {
		t.Error("bind 的相对 source 应被拒绝")
	}

	good := `services:
  web:
    image: nginx
    volumes:
      - type: bind
        source: /srv/data
        target: /data
`
	if got := ScanImportSafety(good); len(got) != 0 {
		t.Errorf("bind 的绝对 source 不应被拒绝，实际 %v", got)
	}
}

// TestScanNeverEchoesScalarValues 是最重要的安全断言：
// 阻塞文案**绝不能**包含用户提供的路径或值。
//
// 危害：`volumes: - ${SECRET_PATH}:/data` 这类写法里，标量可能被插值成密钥；
// 一旦回显，它就进了 Hub 返回体、Web 界面和审计日志的持久存储。
// 行号足以让用户自己定位。
func TestScanNeverEchoesScalarValues(t *testing.T) {
	const secret = "SUPER-SECRET-PATH-abc123"
	cases := []string{
		"services:\n  web:\n    image: nginx\n    volumes:\n      - ./" + secret + ":/data\n",
		"services:\n  web:\n    image: nginx\n    env_file: ./" + secret + "\n",
		"services:\n  web:\n    image: nginx\n    build: ./" + secret + "\n",
	}
	for _, c := range cases {
		for _, b := range ScanImportSafety(c) {
			line := b.String()
			if strings.Contains(line, secret) {
				t.Errorf("阻塞文案泄漏了标量值：%q", line)
			}
		}
	}
}

// TestScanRejectsOversizedContent 验证超大内容被拒（解析炸弹防护）。
func TestScanRejectsOversizedContent(t *testing.T) {
	big := "services:\n  web:\n    image: nginx\n" + strings.Repeat("# filler\n", maxScanBytes/8)
	if got := ScanImportSafety(big); len(got) == 0 {
		t.Error("超过大小上限的内容应被拒绝")
	}
}

// TestScanRejectsHugeNodeCount 验证节点数上限（深度嵌套/大量服务）。
func TestScanRejectsHugeNodeCount(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("services:\n")
	// 每个服务 3 个节点，构造超过上限的节点数。
	for i := 0; i < maxScanNodes/2; i++ {
		sb.WriteString("  s")
		sb.WriteString(strings.Repeat("x", 1))
		sb.WriteString(itoa(i))
		sb.WriteString(":\n    image: nginx\n")
	}
	got := ScanImportSafety(sb.String())
	if len(got) == 0 {
		t.Error("节点数超过上限的内容应被拒绝")
	}
}

// itoa 是极简整数转字符串（避免为测试引入 strconv 混淆）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
