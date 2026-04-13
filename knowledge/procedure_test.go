package knowledge_test

import (
	"strings"
	"testing"

	. "github.com/XHao/claw-go/knowledge"
)

func TestProcedureStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewProcedureStore(dir)

	err := store.Save("debug-golang", ProcedureFile{
		Name:     "Golang 调试流程",
		Tags:     []string{"debug", "golang"},
		Priority: 10,
		Body:     "遇到 panic 先跑 go test -race",
	})
	if err != nil {
		t.Fatal(err)
	}

	procs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 1 {
		t.Fatalf("want 1 procedure, got %d", len(procs))
	}
	if procs[0].Name != "Golang 调试流程" {
		t.Errorf("unexpected name: %s", procs[0].Name)
	}
}

func TestProcedureStore_FindByTags(t *testing.T) {
	dir := t.TempDir()
	store := NewProcedureStore(dir)

	_ = store.Save("debug-golang", ProcedureFile{
		Name: "Golang 调试", Tags: []string{"debug", "golang"}, Priority: 10, Body: "内容A",
	})
	_ = store.Save("deploy-docker", ProcedureFile{
		Name: "Docker 部署", Tags: []string{"deploy", "docker"}, Priority: 20, Body: "内容B",
	})

	results, err := store.FindByTags([]string{"debug"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if !strings.Contains(results[0].Body, "内容A") {
		t.Errorf("unexpected body: %s", results[0].Body)
	}
}

func TestProcedureStore_AppendToExisting(t *testing.T) {
	dir := t.TempDir()
	store := NewProcedureStore(dir)

	_ = store.Save("debug-golang", ProcedureFile{
		Name: "Golang 调试", Tags: []string{"debug"}, Priority: 10, Body: "旧内容",
	})
	err := store.Append("debug-golang", "新增步骤")
	if err != nil {
		t.Fatal(err)
	}

	procs, _ := store.List()
	if !strings.Contains(procs[0].Body, "旧内容") {
		t.Error("old content should be preserved")
	}
	if !strings.Contains(procs[0].Body, "新增步骤") {
		t.Error("new content should be appended")
	}
}

func TestProcedureStore_AppendCreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	store := NewProcedureStore(dir)

	err := store.Append("new-proc", "第一步：先做这个")
	if err != nil {
		t.Fatal(err)
	}

	procs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 1 {
		t.Fatalf("want 1 procedure, got %d", len(procs))
	}
	if !strings.Contains(procs[0].Body, "第一步") {
		t.Errorf("body missing content: %s", procs[0].Body)
	}
}
