package docfs

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWriteDocumentOverwritesExistingFileWithPayload(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload, err := WriteDocument(path, "# 新内容", "")
	if err != nil {
		t.Fatal(err)
	}
	if payload.Path != path || payload.FileName != "note.md" || payload.Markdown != "# 新内容" || payload.Encoding != EncodingUTF8 {
		t.Fatalf("unexpected payload %+v", payload)
	}
	saved, _ := os.ReadFile(path)
	if string(saved) != "# 新内容" {
		t.Fatalf("saved %q", saved)
	}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestDecodeAndEncodeRoundTripAllEncodings(t *testing.T) {
	text := "标题 😀\nline"
	for _, encoding := range []Encoding{EncodingUTF8, EncodingUTF8BOM, EncodingUTF16LEBOM, EncodingUTF16BEBOM} {
		data, err := Encode(text, encoding)
		if err != nil {
			t.Fatal(err)
		}
		decoded, detected, err := Decode(data)
		if err != nil || decoded != text || detected != encoding {
			t.Fatalf("%s: decoded %q detected %s err %v", encoding, decoded, detected, err)
		}
	}
	gbk, err := Encode("中文内容", EncodingGBK)
	if err != nil {
		t.Fatal(err)
	}
	decoded, detected, err := Decode(gbk)
	if err != nil || decoded != "中文内容" || detected != EncodingGBK {
		t.Fatalf("gbk decoded %q detected %s err %v", decoded, detected, err)
	}
	if _, err := Encode("emoji 😀", EncodingGBK); err == nil {
		t.Fatal("GBK cannot represent emoji")
	}
	if _, _, err := Decode([]byte{0xFF, 0xFE, 0x00}); err == nil {
		t.Fatal("odd UTF-16 length must fail")
	}
	if _, _, err := Decode([]byte{0xFF, 0xFE, 0x00, 0xDC}); err == nil {
		t.Fatal("lone low surrogate must fail")
	}
}

func TestFolderListingIncludesSegmentedTextAndJSONDocuments(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"note.md": "# note", "large.txt": "plain", "data.json": "{}", "ignored.bin": "\x00"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := ListFolderDocuments(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range files {
		names = append(names, file.Name)
	}
	if !reflect.DeepEqual(names, []string{"data.json", "large.txt", "note.md"}) {
		t.Fatalf("names %v", names)
	}
}

func TestDirectorySnapshotReleasesParentDirectoryHandle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "note.md"), []byte("# note"), 0o644)
	snapshots, err := snapshotDirectory(root)
	if err != nil || len(snapshots) != 1 || snapshots[0].name != "note.md" {
		t.Fatalf("snapshots %+v err %v", snapshots, err)
	}
	if err := os.Rename(root, root+".moved"); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryListingIsShallowAndPreservesFilteringSortingAndChevrons(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "a-empty"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "B-visible", "deep"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "node_modules"), 0o755))
	must(os.WriteFile(filepath.Join(root, "B-visible", "deep", "nested.md"), []byte("#"), 0o644))
	must(os.WriteFile(filepath.Join(root, "node_modules", "ignored.md"), []byte("#"), 0o644))
	must(os.WriteFile(filepath.Join(root, "z.md"), []byte("#"), 0o644))
	must(os.WriteFile(filepath.Join(root, "A.txt"), []byte("a"), 0o644))
	must(os.WriteFile(filepath.Join(root, "ignored.bin"), []byte{0}, 0o644))

	entries, err := ListFolderChildren(root, root)
	must(err)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	if !reflect.DeepEqual(names, []string{"a-empty", "B-visible", "A.txt", "z.md"}) {
		t.Fatalf("names %v", names)
	}
	if entries[0].HasChildren || !entries[1].HasChildren || len(entries[1].Children) != 0 {
		t.Fatalf("unexpected chevrons %+v", entries[:2])
	}
}

func TestGitignoreRulesAndNegation(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.md\n!keep.md\ndocs/private/\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "docs", "private"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "drop.md"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(root, "keep.md"), nil, 0o644)
	entries, err := ListFolderChildren(root, "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	if !reflect.DeepEqual(names, []string{"docs", "keep.md"}) {
		t.Fatalf("names %v", names)
	}
	docs, _ := ListFolderChildren(filepath.Join(root, "docs"), root)
	if len(docs) != 0 {
		t.Fatalf("docs/private should be ignored: %+v", docs)
	}
}
