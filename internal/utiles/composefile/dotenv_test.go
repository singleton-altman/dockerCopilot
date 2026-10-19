package composefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDotenv(t *testing.T) {
	content := `# 注释行
TZ=Asia/Shanghai

export DEBUG=true
EMPTY=
QUOTED="value with space"
SINGLE='single quoted'
TRAILING=value # 行尾注释
INVALID-KEY=bad
_HIDDEN=ok
1ST=bad
URL=http://example.com:8080/path
`
	values := ParseDotenv(content)

	expect := map[string]string{
		"TZ":       "Asia/Shanghai",
		"DEBUG":    "true",
		"EMPTY":    "",
		"QUOTED":   "value with space",
		"SINGLE":   "single quoted",
		"TRAILING": "value",
		"_HIDDEN":  "ok",
		"URL":      "http://example.com:8080/path",
	}
	for key, want := range expect {
		if got, ok := values[key]; !ok {
			t.Errorf("缺少变量 %s", key)
		} else if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	// 非法键应被忽略
	for _, key := range []string{"INVALID-KEY", "1ST"} {
		if _, ok := values[key]; ok {
			t.Errorf("非法键 %s 不应被解析", key)
		}
	}
}

func TestDotenvFileAllowed(t *testing.T) {
	store := newStore(t)

	if _, err := store.Save(".env", "SECRET=abc\n"); err != nil {
		t.Fatalf("保存 .env 失败: %v", err)
	}
	if _, err := store.Save(".env.production", "SECRET=prod\n"); err != nil {
		t.Fatalf("保存 .env.production 失败: %v", err)
	}

	content, err := store.Read(".env")
	if err != nil {
		t.Fatalf("读取 .env 失败: %v", err)
	}
	if content != "SECRET=abc\n" {
		t.Fatalf("内容不匹配: %q", content)
	}

	// 列表中应能看到 dotenv 文件并标记
	items, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item.Name == ".env" {
			found = true
			if !item.IsEnv {
				t.Error(".env 应标记为 IsEnv")
			}
		}
	}
	if !found {
		t.Error("列表中没有 .env")
	}
}

func TestDotenvRejectsUnsafeNames(t *testing.T) {
	store := newStore(t)
	for _, name := range []string{".env/../evil", "../.env", ".env.local/x", ".environment"} {
		if _, err := store.Save(name, "A=1\n"); err == nil {
			t.Errorf("应拒绝 %q", name)
		}
	}
}

func TestValidateDotenv(t *testing.T) {
	store := newStore(t)

	// 合法
	if err := store.ValidateContent(".env", "A=1\nB=2\n"); err != nil {
		t.Fatalf("合法 dotenv 被拒: %v", err)
	}

	// 只有注释和空行
	if _, err := store.Save(".env", "# just a comment\n\n"); err != nil {
		t.Fatalf("纯注释 dotenv 应被允许: %v", err)
	}
}

func TestValidateInjectsDotenv(t *testing.T) {
	store := newStore(t)

	// 先写入 .env
	if _, err := store.Save(".env", "MY_SECRET=from_dotenv\n"); err != nil {
		t.Fatal(err)
	}

	// 变量已定义：应通过
	okCompose := "services:\n  web:\n    image: nginx\n    environment:\n      SECRET: ${MY_SECRET}\n"
	if err := store.ValidateContent("app.yaml", okCompose); err != nil {
		t.Fatalf("已定义变量应通过: %v", err)
	}

	// 变量未定义：应被拒绝
	badCompose := "services:\n  web:\n    image: nginx\n    environment:\n      SECRET: ${UNDEFINED_SECRET}\n"
	err := store.ValidateContent("app.yaml", badCompose)
	if err == nil {
		t.Fatal("未定义变量应报错")
	}
	t.Logf("预期错误: %v", err)
}

func TestValidateDefaultValueStillWorks(t *testing.T) {
	store := newStore(t)
	compose := "services:\n  web:\n    image: nginx:${TAG:-latest}\n"
	if err := store.ValidateContent("app.yaml", compose); err != nil {
		t.Fatalf("带默认值的变量应通过: %v", err)
	}
}

func TestDotenvNotBackedUpAsCompose(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if _, err := store.Save(".env", "A=1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(".env", "A=2\n"); err != nil {
		t.Fatal(err)
	}

	backups, err := os.ReadDir(filepath.Join(dir, backupDirName))
	if err != nil {
		t.Fatal(err)
	}
	// 备份机制同样适用于 .env，确保改动可回溯
	if len(backups) == 0 {
		t.Fatal(".env 覆盖时应产生备份")
	}
}
