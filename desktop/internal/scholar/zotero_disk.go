package scholar

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/util/vfsutil"
	"github.com/ncruces/go-sqlite3/vfs"
)

var errZoteroReadOnly = errors.New("Zotero 本地目录为只读，请通过 Zotero 本地接口执行此操作")

const zoteroDiskVFSName = "nomo-zotero-readonly"

// Catalog, inline citations and the PDF reader share one short-lived connection at a time.
var zoteroDiskGate = make(chan struct{}, 1)

// WAL mode can write a shared-memory index even on a query-only connection.
// Let SQLite hold an exclusive database lock and keep that index in memory.
type zoteroDiskVFS struct{ vfs.VFSFilename }

func (d zoteroDiskVFS) FullPathname(path string) (string, error) {
	if runtime.GOOS == "windows" && len(path) > 3 && path[0] == '/' && path[2] == ':' && path[3] == '/' {
		path = path[1:]
	}
	return d.VFSFilename.FullPathname(filepath.FromSlash(path))
}

func (d zoteroDiskVFS) Open(_ string, flags vfs.OpenFlag) (vfs.File, vfs.OpenFlag, error) {
	return nil, flags, sqlite3.CANTOPEN
}

func (d zoteroDiskVFS) OpenFilename(name *vfs.Filename, flags vfs.OpenFlag) (vfs.File, vfs.OpenFlag, error) {
	readFlags := flags &^ (vfs.OPEN_READWRITE | vfs.OPEN_CREATE | vfs.OPEN_DELETEONCLOSE)
	readFlags |= vfs.OPEN_READONLY
	openFlags := readFlags
	switch {
	case flags&vfs.OPEN_MAIN_DB != 0:
		// The OS handle must permit a write lock. SQLite still receives a
		// read-only file whose write, truncate and mmap paths are blocked.
		openFlags = (readFlags &^ vfs.OPEN_READONLY) | vfs.OPEN_READWRITE
	case flags&(vfs.OPEN_WAL|vfs.OPEN_MAIN_JOURNAL) != 0:
	default:
		return nil, readFlags, sqlite3.CANTOPEN
	}
	file, _, err := d.VFSFilename.OpenFilename(name, openFlags)
	if flags&vfs.OPEN_WAL != 0 && errors.Is(err, fs.ErrNotExist) {
		// A clean shutdown can remove the WAL while leaving the main file
		// in WAL mode. SQLite already owns the exclusive lock at this point.
		return zoteroDiskFile{&vfsutil.SliceFile{}}, readFlags, nil
	}
	if err != nil {
		return nil, readFlags, err
	}
	return zoteroDiskFile{file}, readFlags, nil
}

func (zoteroDiskVFS) Delete(string, bool) error { return sqlite3.READONLY }

// Embed only the base interface, so SQLite cannot map files or open -shm.
type zoteroDiskFile struct{ vfs.File }

func (zoteroDiskFile) WriteAt([]byte, int64) (int, error) { return 0, sqlite3.READONLY }
func (zoteroDiskFile) Truncate(int64) error               { return sqlite3.READONLY }
func (zoteroDiskFile) Sync(vfs.SyncFlag) error            { return sqlite3.READONLY }

func init() {
	vfs.Register(zoteroDiskVFSName, zoteroDiskVFS{vfs.Find("os").(vfs.VFSFilename)})
}

func OpenZoteroDirectory(dir string) (Zotero, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Zotero{}, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return Zotero{}, err
	}
	info, err := os.Stat(filepath.Join(dir, "zotero.sqlite"))
	if err != nil {
		return Zotero{}, fmt.Errorf("无法打开所选目录中的 zotero.sqlite：%w", err)
	}
	if !info.Mode().IsRegular() {
		return Zotero{}, fmt.Errorf("所选目录中的 zotero.sqlite 不是普通文件")
	}
	return Zotero{diskDir: dir}, nil
}

func (z Zotero) ReadOnly() bool { return z.diskDir != "" }

func (z Zotero) CanWrite() bool { return !z.ReadOnly() && z.ServerID != "" && z.writeKey != nil }

func (z Zotero) SameSource(other Zotero) bool {
	return z.ServerID == other.ServerID && z.diskDir == other.diskDir
}

func (z Zotero) withDisk(ctx context.Context, read func(*sqlite3.Conn) error) error {
	path := filepath.ToSlash(filepath.Join(z.diskDir, "zotero.sqlite"))
	if runtime.GOOS == "windows" && len(path) > 2 && path[1] == ':' {
		path = "/" + path
	}
	uri := url.URL{Scheme: "file", Path: path}
	uri.RawQuery = url.Values{"mode": {"ro"}, "vfs": {zoteroDiskVFSName}}.Encode()
	// SQLite URI authorities do not accept UNC server names. An empty
	// authority keeps a UNC path in the path component of the URI.
	filename := uri.String()
	if strings.HasPrefix(path, "//") {
		filename = "file://" + uri.EscapedPath() + "?" + uri.RawQuery
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	select {
	case zoteroDiskGate <- struct{}{}:
		defer func() { <-zoteroDiskGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	conn, err := sqlite3.OpenContext(sqlite3.WithMaxMemory(ctx, 16<<20), filename)
	if err == nil {
		defer conn.Close()
		conn.SetInterrupt(ctx)
		_, err = conn.Config(sqlite3.DBCONFIG_NO_CKPT_ON_CLOSE, true)
		if err == nil {
			err = conn.Exec("PRAGMA locking_mode=EXCLUSIVE; PRAGMA query_only=ON; PRAGMA trusted_schema=OFF; PRAGMA temp_store=MEMORY; PRAGMA cache_size=-1024; BEGIN")
		}
		if err == nil {
			err = read(conn)
		}
	}
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, sqlite3.BUSY), errors.Is(err, sqlite3.LOCKED), errors.Is(err, sqlite3.READONLY_RECOVERY):
		return fmt.Errorf("Zotero 数据库正被占用，请退出 Zotero 后重试，或连接本地接口：%w", err)
	case err != nil:
		return fmt.Errorf("读取 Zotero 本地目录失败：%w", err)
	default:
		return nil
	}
}

func zoteroDiskQuery(conn *sqlite3.Conn, sql string, row func(*sqlite3.Stmt) error, args ...any) error {
	stmt, _, err := conn.Prepare(sql)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, arg := range args {
		switch arg := arg.(type) {
		case int:
			err = stmt.BindInt(i+1, arg)
		case string:
			err = stmt.BindText(i+1, arg)
		default:
			return fmt.Errorf("unsupported Zotero query argument %T", arg)
		}
		if err != nil {
			return err
		}
	}
	for stmt.Step() {
		if err := row(stmt); err != nil {
			return err
		}
	}
	return stmt.Err()
}

func (z Zotero) diskLibraryID(conn *sqlite3.Conn) (int, error) {
	id := 0
	sql, args := "SELECT libraryID FROM libraries WHERE type='user' LIMIT 1", []any{}
	if z.Group != 0 {
		sql, args = "SELECT libraryID FROM groups WHERE groupID=?", []any{z.Group}
	}
	err := zoteroDiskQuery(conn, sql, func(row *sqlite3.Stmt) error {
		id = row.ColumnInt(0)
		return nil
	}, args...)
	if err == nil && id <= 0 {
		err = fmt.Errorf("找不到所选 Zotero 文献库")
	}
	return id, err
}

func (z Zotero) diskLibraries(ctx context.Context) ([]ZoteroLibrary, error) {
	libraries := []ZoteroLibrary{{0, "Personal library"}}
	err := z.withDisk(ctx, func(conn *sqlite3.Conn) error {
		return zoteroDiskQuery(conn, "SELECT groupID, substr(name,1,256) FROM groups ORDER BY name COLLATE NOCASE, groupID LIMIT 200", func(row *sqlite3.Stmt) error {
			libraries = append(libraries, ZoteroLibrary{row.ColumnInt(0), cleanField(row.ColumnText(1), 256)})
			return nil
		})
	})
	return libraries, err
}

func (z Zotero) diskCollections(ctx context.Context, parent string, start int) (ZoteroPage, error) {
	page := ZoteroPage{}
	err := z.withDisk(ctx, func(conn *sqlite3.Conn) error {
		library, err := z.diskLibraryID(conn)
		if err != nil {
			return err
		}
		return zoteroDiskQuery(conn, `
			SELECT c.key, substr(c.collectionName,1,512), coalesce(p.key,'')
			FROM collections c LEFT JOIN collections p ON p.collectionID=c.parentCollectionID AND p.libraryID=c.libraryID
			WHERE c.libraryID=? AND ((?='' AND c.parentCollectionID IS NULL) OR p.key=?)
			  AND NOT EXISTS (SELECT 1 FROM deletedCollections d WHERE d.collectionID=c.collectionID)
			ORDER BY c.collectionName COLLATE NOCASE, c.collectionID LIMIT ? OFFSET ?`, func(row *sqlite3.Stmt) error {
			if len(page.Collections) == ZoteroPageSize {
				page.More = true
				return nil
			}
			page.Collections = append(page.Collections, Collection{row.ColumnText(0), cleanField(row.ColumnText(1), 512), row.ColumnText(2)})
			return nil
		}, library, parent, parent, ZoteroPageSize+1, start)
	})
	page.Next = start + len(page.Collections)
	return page, err
}

func (z Zotero) diskTags(ctx context.Context, query string, start int) ([]string, bool, error) {
	var tags []string
	more := false
	err := z.withDisk(ctx, func(conn *sqlite3.Conn) error {
		library, err := z.diskLibraryID(conn)
		if err != nil {
			return err
		}
		return zoteroDiskQuery(conn, `
			SELECT t.name FROM tags t WHERE length(t.name)<=512 AND instr(lower(t.name),lower(?))>0
			  AND EXISTS (SELECT 1 FROM itemTags it JOIN items i ON i.itemID=it.itemID
			              WHERE it.tagID=t.tagID AND i.libraryID=?
			                AND NOT EXISTS (SELECT 1 FROM deletedItems d WHERE d.itemID=i.itemID))
			ORDER BY t.name COLLATE NOCASE, t.tagID LIMIT ? OFFSET ?`, func(row *sqlite3.Stmt) error {
			if len(tags) == ZoteroPageSize {
				more = true
			} else {
				tags = append(tags, row.ColumnText(0))
			}
			return nil
		}, cleanField(query, 512), library, ZoteroPageSize+1, start)
	})
	return tags, more, err
}
