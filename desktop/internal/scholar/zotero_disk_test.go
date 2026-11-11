package scholar

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ncruces/go-sqlite3"
)

func TestZoteroDirectoryReadsWithoutWriting(t *testing.T) {
	for _, mode := range []string{"checkpointed", "pending-wal"} {
		t.Run(mode, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "本地文库 #1")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			db := filepath.Join(dir, "zotero.sqlite")
			conn, err := sqlite3.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.Exec(zoteroDiskFixture); err != nil {
				t.Fatal(err)
			}
			if mode == "pending-wal" {
				if _, err := conn.Config(sqlite3.DBCONFIG_NO_CKPT_ON_CLOSE, true); err != nil {
					t.Fatal(err)
				}
			}
			if err := conn.Exec(`INSERT INTO items VALUES (41,1,'2026-01-01','2026-11-10',1,'WALITEM1');
				INSERT INTO itemData VALUES (41,1,7)`); err != nil {
				t.Fatal(err)
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			if mode == "pending-wal" {
				info, err := os.Stat(db + "-wal")
				if err != nil || info.Size() == 0 {
					t.Fatalf("fixture has no pending WAL: %v", err)
				}
				// Reopening after an unclean shutdown must work without an SHM file.
				if err := os.Remove(db + "-shm"); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			}
			before := zoteroFileState(t, dir)
			z, err := OpenZoteroDirectory(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !z.ReadOnly() || z.CanWrite() {
				t.Fatal("directory exposed a writable Zotero connection")
			}
			ctx := context.Background()
			page, err := z.Browse(ctx, "WAL-only", "", "", ZoteroAll, 0)
			if err != nil || len(page.Entries) != 1 || page.Entries[0].RemoteID != "WALITEM1" {
				t.Fatalf("committed data is missing: %+v, %v", page, err)
			}
			if err := z.Save(ctx, Entry{Title: "Denied"}); !errors.Is(err, errZoteroReadOnly) {
				t.Fatalf("save did not enforce read-only mode: %v", err)
			}
			if err := z.SetItemTrashed(ctx, "WALITEM1", true); !errors.Is(err, errZoteroReadOnly) {
				t.Fatalf("trash did not enforce read-only mode: %v", err)
			}
			after := zoteroFileState(t, dir)
			if len(after) != len(before) {
				t.Fatalf("directory entries changed: before %d, after %d", len(before), len(after))
			}
			for name, file := range before {
				current, ok := after[name]
				if !ok || file.modified != current.modified || !bytes.Equal(file.data, current.data) {
					t.Errorf("%s changed after a read-only session", name)
				}
			}
		})
	}
}

func TestZoteroDirectoryCatalog(t *testing.T) {
	dir := t.TempDir()
	conn, err := sqlite3.Open(filepath.Join(dir, "zotero.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Exec(zoteroDiskFixture); err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec(`
		WITH RECURSIVE rows(n) AS (VALUES(100) UNION ALL SELECT n+1 FROM rows WHERE n<125)
		INSERT INTO items SELECT n,1,'2026-01-01','2026-01-01',1,printf('%08d',n) FROM rows;
		INSERT INTO itemData SELECT itemID,1,1 FROM items WHERE itemID>=100`); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "storage", "PDFITEM1", "paper.pdf")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("%PDF-1.4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	z, err := OpenZoteroDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	checkPage := func(query, collection, tag string, scope ZoteroScope, start, size int, more bool) ZoteroPage {
		t.Helper()
		page, err := z.Browse(ctx, query, collection, tag, scope, start)
		if err != nil || len(page.Entries) != size || page.More != more {
			t.Fatalf("query %q: got %d entries and more=%t, error %v", query, len(page.Entries), page.More, err)
		}
		return page
	}
	page := checkPage("研报", "", "", ZoteroAll, 0, 25, true)
	checkPage("研报", "", "", ZoteroAll, page.Next, 2, false)
	page = checkPage("Chen 2026", "COLLECT1", "可复现", ZoteroAll, 0, 1, false)
	if page.Entries[0].RemoteID != "PAPER001" {
		t.Fatalf("unexpected item: %s", page.Entries[0].RemoteID)
	}
	checkPage("已删除", "", "", ZoteroAll, 0, 0, false)
	checkPage("已删除", "", "", ZoteroTrash, 0, 1, false)
	collections, err := z.Collections(ctx, 0)
	if err != nil || len(collections.Collections) != 1 || collections.Collections[0].Key != "COLLECT1" {
		t.Fatalf("invalid collections: %+v, %v", collections, err)
	}
	tags, more, err := z.Tags(ctx, "复现", 0)
	if err != nil || more || len(tags) != 1 || tags[0] != "可复现" {
		t.Fatalf("invalid tags: %q, %t, %v", tags, more, err)
	}
	detail, err := z.Detail(ctx, "PAPER001", 0)
	if err != nil || len(detail.Notes) != 1 || detail.Notes[0].Text != "一条笔记" ||
		len(detail.PDFs) != 1 || detail.PDFs[0].Key != "PDFITEM1" || len(detail.CollectionInfo) != 1 {
		t.Fatalf("invalid item detail: %+v, %v", detail, err)
	}
	gotPath, err := z.AttachmentPath(ctx, "PDFITEM1")
	if err != nil || gotPath != filepath.Join(z.diskDir, "storage", "PDFITEM1", "paper.pdf") {
		t.Fatalf("attachment resolution: %q, %v", gotPath, err)
	}
	notes, more, err := z.Annotations(ctx, "PDFITEM1", 0)
	if err != nil || more || len(notes) != 1 || notes[0].Page != 2 || notes[0].Label != "iv" {
		t.Fatalf("invalid annotation locator: %+v, %t, %v", notes, more, err)
	}
	libraries, err := z.Libraries(ctx)
	if err != nil || len(libraries) != 2 || libraries[1].ID != 123 {
		t.Fatalf("invalid library list: %+v, %v", libraries, err)
	}
	z.Group = 123
	page = checkPage("", "", "", ZoteroAll, 0, 1, false)
	if page.Entries[0].Title != "群组文献" {
		t.Fatalf("group query resolved a personal item: %+v", page)
	}
	if _, err := z.AttachmentPath(ctx, "PDFITEM1"); err == nil {
		t.Fatal("group query accessed a personal attachment")
	}
}

type zoteroFileSnapshot struct {
	modified int64
	data     []byte
}

func zoteroFileState(t *testing.T, dir string) map[string]zoteroFileSnapshot {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	state := make(map[string]zoteroFileSnapshot, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		state[entry.Name()] = zoteroFileSnapshot{info.ModTime().UnixNano(), data}
	}
	return state
}

const zoteroDiskFixture = `
PRAGMA journal_mode=WAL;
CREATE TABLE libraries(libraryID INTEGER PRIMARY KEY, type TEXT);
CREATE TABLE groups(groupID INTEGER PRIMARY KEY, libraryID INTEGER, name TEXT);
CREATE TABLE items(itemID INTEGER PRIMARY KEY, itemTypeID INTEGER, dateAdded TEXT, dateModified TEXT, libraryID INTEGER, key TEXT);
CREATE TABLE itemTypesCombined(itemTypeID INTEGER PRIMARY KEY, typeName TEXT);
CREATE TABLE itemData(itemID INTEGER, fieldID INTEGER, valueID INTEGER);
CREATE TABLE itemDataValues(valueID INTEGER PRIMARY KEY, value TEXT);
CREATE TABLE fieldsCombined(fieldID INTEGER PRIMARY KEY, fieldName TEXT);
CREATE TABLE itemCreators(itemID INTEGER, creatorID INTEGER, creatorTypeID INTEGER, orderIndex INTEGER);
CREATE TABLE creators(creatorID INTEGER PRIMARY KEY, firstName TEXT, lastName TEXT);
CREATE TABLE creatorTypes(creatorTypeID INTEGER PRIMARY KEY, creatorType TEXT);
CREATE TABLE itemNotes(itemID INTEGER PRIMARY KEY, parentItemID INTEGER, note TEXT, title TEXT);
CREATE TABLE itemAttachments(itemID INTEGER PRIMARY KEY, parentItemID INTEGER, linkMode INTEGER, contentType TEXT, path TEXT);
CREATE TABLE itemAnnotations(itemID INTEGER PRIMARY KEY, parentItemID INTEGER, type INTEGER, text TEXT, comment TEXT, pageLabel TEXT, sortIndex TEXT, position TEXT);
CREATE TABLE collections(collectionID INTEGER PRIMARY KEY, collectionName TEXT, parentCollectionID INTEGER, libraryID INTEGER, key TEXT);
CREATE TABLE collectionItems(collectionID INTEGER, itemID INTEGER);
CREATE TABLE tags(tagID INTEGER PRIMARY KEY, name TEXT);
CREATE TABLE itemTags(itemID INTEGER, tagID INTEGER);
CREATE TABLE deletedItems(itemID INTEGER PRIMARY KEY);
CREATE TABLE deletedCollections(collectionID INTEGER PRIMARY KEY);
CREATE TABLE publicationsItems(itemID INTEGER PRIMARY KEY);
INSERT INTO libraries VALUES (1,'user'),(5,'group');
INSERT INTO groups VALUES (123,5,'研究组');
INSERT INTO itemTypesCombined VALUES (1,'journalArticle'),(2,'note'),(3,'attachment'),(4,'annotation');
INSERT INTO items VALUES
	(1,1,'2026-01-01','2026-11-09',1,'PAPER001'),
	(2,2,'2026-01-01','2026-11-09',1,'NOTE0001'),
	(3,3,'2026-01-01','2026-11-09',1,'PDFITEM1'),
	(4,4,'2026-01-01','2026-11-09',1,'ANNOT001'),
	(5,1,'2026-01-01','2026-11-09',1,'TRASH001'),
	(6,1,'2026-01-01','2026-11-09',5,'PAPER001');
INSERT INTO fieldsCombined VALUES (1,'title'),(2,'date'),(3,'citationKey');
INSERT INTO itemDataValues VALUES (1,'一份研报'),(2,'2026-02-03'),(3,'paper.pdf'),(4,'已删除'),(5,'群组文献'),(6,'chen2026'),(7,'WAL-only commit');
INSERT INTO itemData VALUES (1,1,1),(1,2,2),(1,3,6),(3,1,3),(5,1,4),(6,1,5);
INSERT INTO creators VALUES (1,'Ming','Chen');
INSERT INTO creatorTypes VALUES (1,'author');
INSERT INTO itemCreators VALUES (1,1,1,0);
INSERT INTO itemNotes VALUES (2,1,'<p>一条笔记</p>','一条笔记');
INSERT INTO itemAttachments VALUES (3,1,0,'application/pdf','storage:paper.pdf');
INSERT INTO itemAnnotations VALUES (4,3,1,'一段摘录','','iv','00001','{"pageIndex":1}');
INSERT INTO collections VALUES (1,'主分类',NULL,1,'COLLECT1'),(2,'已删除分类',NULL,1,'COLLECT2');
INSERT INTO collectionItems VALUES (1,1);
INSERT INTO tags VALUES (1,'可复现');
INSERT INTO itemTags VALUES (1,1);
INSERT INTO deletedItems VALUES (5);
INSERT INTO deletedCollections VALUES (2);`
