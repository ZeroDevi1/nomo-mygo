package scholar

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ncruces/go-sqlite3"
)

const zoteroDiskItems = `
	FROM items i JOIN itemTypesCombined t ON t.itemTypeID=i.itemTypeID
	LEFT JOIN itemNotes n ON n.itemID=i.itemID
	LEFT JOIN itemAttachments a ON a.itemID=i.itemID
	LEFT JOIN deletedItems d ON d.itemID=i.itemID`

const zoteroDiskColumns = `
	i.itemID, i.key, t.typeName, coalesce(d.itemID,0), i.dateAdded, i.dateModified,
	substr(n.title,1,1024), substr(n.note,1,?), coalesce(a.linkMode,-1), a.contentType,
	(SELECT json_group_object(name,value) FROM (
		SELECT f.fieldName AS name, substr(CAST(v.value AS TEXT),1,8192) AS value
		FROM itemData m JOIN fieldsCombined f ON f.fieldID=m.fieldID
		JOIN itemDataValues v ON v.valueID=m.valueID
		WHERE m.itemID=i.itemID AND (? OR f.fieldName IN
			('title','caseName','subject','nameOfAct','date','publicationTitle','proceedingsTitle','bookTitle','DOI','citationKey','abstractNote','url'))
		ORDER BY m.fieldID LIMIT 64)),
	(SELECT json_group_array(json_object('firstName',firstName,'lastName',lastName,'creatorType',role)) FROM (
		SELECT substr(c.firstName,1,128) AS firstName, substr(c.lastName,1,128) AS lastName, ct.creatorType AS role
		FROM itemCreators ic JOIN creators c ON c.creatorID=ic.creatorID
		JOIN creatorTypes ct ON ct.creatorTypeID=ic.creatorTypeID
		WHERE ic.itemID=i.itemID ORDER BY ic.orderIndex LIMIT 64)),
	EXISTS (SELECT 1 FROM itemAttachments child JOIN items ci ON ci.itemID=child.itemID
	        WHERE child.parentItemID=i.itemID AND ci.libraryID=i.libraryID AND child.contentType='application/pdf'
	          AND NOT EXISTS (SELECT 1 FROM deletedItems cd WHERE cd.itemID=ci.itemID))
	OR EXISTS (SELECT 1 FROM itemNotes child JOIN items ci ON ci.itemID=child.itemID
	           WHERE child.parentItemID=i.itemID AND ci.libraryID=i.libraryID
	             AND NOT EXISTS (SELECT 1 FROM deletedItems cd WHERE cd.itemID=ci.itemID)),
	coalesce(a.path,'')='' OR a.path LIKE 'attachments:%'`

func zoteroDiskItem(row *sqlite3.Stmt) (zoteroItem, error) {
	var item zoteroItem
	var fields struct {
		ProceedingsTitle, BookTitle string
	}
	raw := []byte(row.ColumnText(10))
	if err := json.Unmarshal(raw, &item.Data); err != nil {
		return item, err
	}
	if err := json.Unmarshal([]byte(row.ColumnText(11)), &item.Data.Creators); err != nil {
		return item, err
	}
	if item.Data.PublicationTitle == "" {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return item, err
		}
		item.Data.PublicationTitle = fields.ProceedingsTitle
		if item.Data.PublicationTitle == "" {
			item.Data.PublicationTitle = fields.BookTitle
		}
	}
	item.Key, item.Data.ItemType, item.Data.Deleted = row.ColumnText(1), row.ColumnText(2), row.ColumnBool(3)
	item.Data.DateAdded, item.Data.DateModified = row.ColumnText(4), row.ColumnText(5)
	if item.Data.ItemType == "note" {
		item.Data.Title, item.Data.Note = row.ColumnText(6), row.ColumnText(7)
	}
	if row.ColumnInt(8) == 3 { // Zotero's linked_url attachment mode
		item.Data.LinkMode = "linked_url"
	}
	item.Data.ContentType = row.ColumnText(9)
	item.Meta.NumChildren = row.ColumnInt(12)
	item.unavailablePDF = row.ColumnBool(13)
	return item, nil
}

func (z Zotero) diskSearch(ctx context.Context, query, collection, tag string, scope ZoteroScope, browse bool, start int) (ZoteroPage, error) {
	page := ZoteroPage{}
	if collection != "" && !zoteroKey.MatchString(collection) {
		return page, fmt.Errorf("无效 Zotero 分类键")
	}
	err := z.withDisk(ctx, func(conn *sqlite3.Conn) error {
		library, err := z.diskLibraryID(conn)
		if err != nil {
			return err
		}
		args := []any{512, 0, library}
		filter := strings.Builder{}
		filter.WriteString(" WHERE i.libraryID=? AND t.typeName<>'annotation' AND (t.typeName<>'attachment' OR a.contentType='application/pdf')")
		if !browse {
			filter.WriteString(" AND t.typeName<>'note'")
		}
		if scope == ZoteroTrash {
			filter.WriteString(" AND d.itemID IS NOT NULL")
		} else {
			filter.WriteString(" AND d.itemID IS NULL AND n.parentItemID IS NULL AND a.parentItemID IS NULL")
		}
		switch scope {
		case ZoteroPublications:
			filter.WriteString(" AND EXISTS (SELECT 1 FROM publicationsItems p WHERE p.itemID=i.itemID)")
		case ZoteroUnfiled:
			filter.WriteString(" AND NOT EXISTS (SELECT 1 FROM collectionItems ci JOIN collections c ON c.collectionID=ci.collectionID AND c.libraryID=i.libraryID WHERE ci.itemID=i.itemID AND NOT EXISTS (SELECT 1 FROM deletedCollections dc WHERE dc.collectionID=c.collectionID))")
		}
		if collection != "" {
			filter.WriteString(" AND EXISTS (SELECT 1 FROM collectionItems ci JOIN collections c ON c.collectionID=ci.collectionID AND c.libraryID=i.libraryID WHERE ci.itemID=i.itemID AND c.key=? AND NOT EXISTS (SELECT 1 FROM deletedCollections dc WHERE dc.collectionID=c.collectionID))")
			args = append(args, collection)
		}
		if tag != "" {
			filter.WriteString(" AND EXISTS (SELECT 1 FROM itemTags it JOIN tags tg ON tg.tagID=it.tagID WHERE it.itemID=i.itemID AND tg.name=?)")
			args = append(args, tag)
		}
		words := strings.Fields(cleanField(query, 512))
		for _, word := range words[:min(len(words), 16)] {
			filter.WriteString(` AND (EXISTS (
				SELECT 1 FROM itemData m JOIN fieldsCombined f ON f.fieldID=m.fieldID
				JOIN itemDataValues v ON v.valueID=m.valueID WHERE m.itemID=i.itemID
				  AND f.fieldName IN ('title','caseName','subject','nameOfAct','date','DOI','citationKey','abstractNote')
				  AND instr(lower(v.value),lower(?))>0)
				OR EXISTS (SELECT 1 FROM itemCreators ic JOIN creators c ON c.creatorID=ic.creatorID
				           WHERE ic.itemID=i.itemID AND instr(lower(concat_ws(' ',c.firstName,c.lastName)),lower(?))>0)
				OR instr(lower(n.title),lower(?))>0)`)
			args = append(args, word, word, word)
		}
		args = append(args, ZoteroPageSize+1, start)
		count := 0
		return zoteroDiskQuery(conn, "SELECT "+zoteroDiskColumns+zoteroDiskItems+filter.String()+
			" ORDER BY i.dateModified DESC, i.itemID DESC LIMIT ? OFFSET ?", func(row *sqlite3.Stmt) error {
			if count == ZoteroPageSize {
				page.More = true
				return nil
			}
			item, err := zoteroDiskItem(row)
			if err != nil {
				return err
			}
			if entry, ok := z.entry(item); ok {
				entry.Ordinal = start + count
				page.Entries = append(page.Entries, entry)
			}
			count++
			page.Next = start + count
			return nil
		}, args...)
	})
	return page, err
}

func (z Zotero) diskItem(conn *sqlite3.Conn, key string, detail bool) (id int, item zoteroItem, fields []ZoteroField, err error) {
	library, err := z.diskLibraryID(conn)
	if err != nil {
		return 0, item, nil, err
	}
	full, noteLimit := 0, 512
	if detail {
		full, noteLimit = 1, 32<<10
	}
	err = zoteroDiskQuery(conn, "SELECT "+zoteroDiskColumns+zoteroDiskItems+" WHERE i.libraryID=? AND i.key=?", func(row *sqlite3.Stmt) error {
		id = row.ColumnInt(0)
		item, err = zoteroDiskItem(row)
		if err == nil && detail {
			var metadata map[string]string
			err = json.Unmarshal([]byte(row.ColumnText(10)), &metadata)
			for name, value := range metadata {
				if name != "abstractNote" {
					fields = append(fields, ZoteroField{name, name, cleanField(value, 8192)})
				}
			}
		}
		return err
	}, noteLimit, full, library, key)
	if err == nil && id == 0 {
		err = fmt.Errorf("找不到 Zotero 条目 %s", key)
	}
	return
}

func (z Zotero) diskEntry(ctx context.Context, key string) (Entry, error) {
	var entry Entry
	err := z.withDisk(ctx, func(conn *sqlite3.Conn) error {
		_, item, _, err := z.diskItem(conn, key, false)
		if err != nil {
			return err
		}
		var ok bool
		entry, ok = z.entry(item)
		if !ok {
			return fmt.Errorf("无效 Zotero 条目")
		}
		return nil
	})
	return entry, err
}

func (z Zotero) diskDetail(ctx context.Context, key string, start int) (ZoteroDetail, error) {
	var detail ZoteroDetail
	err := z.withDisk(ctx, func(conn *sqlite3.Conn) error {
		id, item, fields, err := z.diskItem(conn, key, true)
		if err != nil {
			return err
		}
		detail = z.detailMetadata(item)
		slices.SortFunc(fields, func(a, b ZoteroField) int { return strings.Compare(a.Name, b.Name) })
		remaining := 32 << 10
		for _, field := range fields {
			field.Value = cleanField(field.Value, remaining)
			detail.Fields = append(detail.Fields, field)
			remaining -= len(field.Value)
			if remaining <= 0 {
				break
			}
		}
		detail.Tags, err = zoteroDiskItemTags(conn, id)
		if err != nil {
			return err
		}
		if item.Data.ItemType == "note" {
			note := z.note(item)
			note.Tags = detail.Tags
			detail.Notes = []ZoteroNote{note}
			return nil
		}
		if item.Data.Deleted {
			return nil
		}
		if item.Data.ItemType == "attachment" && item.Data.ContentType == "application/pdf" {
			detail.PDFs = []Attachment{item.attachment()}
			return nil
		}
		if err := zoteroDiskQuery(conn, `
			SELECT c.key, substr(c.collectionName,1,256), coalesce(p.key,'')
			FROM collectionItems ci JOIN items i ON i.itemID=ci.itemID
			JOIN collections c ON c.collectionID=ci.collectionID AND c.libraryID=i.libraryID
			LEFT JOIN collections p ON p.collectionID=c.parentCollectionID AND p.libraryID=c.libraryID
			WHERE ci.itemID=? AND NOT EXISTS (SELECT 1 FROM deletedCollections d WHERE d.collectionID=c.collectionID)
			ORDER BY c.collectionName COLLATE NOCASE, c.collectionID LIMIT 32`, func(row *sqlite3.Stmt) error {
			collection := Collection{row.ColumnText(0), cleanField(row.ColumnText(1), 256), row.ColumnText(2)}
			detail.Collections = append(detail.Collections, collection.Key)
			detail.CollectionInfo = append(detail.CollectionInfo, collection)
			return nil
		}, id); err != nil {
			return err
		}
		count := 0
		return zoteroDiskQuery(conn, "SELECT "+zoteroDiskColumns+zoteroDiskItems+`
			WHERE i.libraryID=(SELECT libraryID FROM items WHERE itemID=?) AND d.itemID IS NULL
			  AND i.itemID IN (
			      SELECT itemID FROM itemNotes WHERE parentItemID=?
			      UNION ALL
			      SELECT itemID FROM itemAttachments WHERE parentItemID=? AND contentType='application/pdf')
			ORDER BY CASE WHEN a.contentType='application/pdf' AND coalesce(a.path,'')<>'' THEN 0 ELSE 1 END,
			         i.dateAdded, i.itemID LIMIT ? OFFSET ?`, func(row *sqlite3.Stmt) error {
			if count == ZoteroPageSize {
				detail.More = true
				return nil
			}
			count++
			child, err := zoteroDiskItem(row)
			if err != nil {
				return err
			}
			if !zoteroKey.MatchString(child.Key) {
				return nil
			}
			if child.Data.ItemType == "note" {
				note := z.note(child)
				note.Tags, err = zoteroDiskItemTags(conn, row.ColumnInt(0))
				detail.Notes = append(detail.Notes, note)
			} else if child.Data.ContentType == "application/pdf" {
				detail.PDFs = append(detail.PDFs, child.attachment())
			}
			return err
		}, 32<<10, 0, id, id, id, ZoteroPageSize+1, start)
	})
	return detail, err
}

func zoteroDiskItemTags(conn *sqlite3.Conn, id int) ([]string, error) {
	var tags []string
	err := zoteroDiskQuery(conn, `SELECT substr(t.name,1,256) FROM itemTags it JOIN tags t ON t.tagID=it.tagID
		WHERE it.itemID=? ORDER BY t.name COLLATE NOCASE LIMIT 32`, func(row *sqlite3.Stmt) error {
		tags = append(tags, cleanField(row.ColumnText(0), 256))
		return nil
	}, id)
	return tags, err
}

func (z Zotero) diskAttachmentPath(ctx context.Context, key string) (string, error) {
	var path string
	err := z.withDisk(ctx, func(conn *sqlite3.Conn) error {
		library, err := z.diskLibraryID(conn)
		if err != nil {
			return err
		}
		return zoteroDiskQuery(conn, `
			SELECT substr(a.path,1,8192) FROM itemAttachments a JOIN items i ON i.itemID=a.itemID
			WHERE i.libraryID=? AND i.key=? AND a.contentType='application/pdf'
			  AND NOT EXISTS (SELECT 1 FROM deletedItems d WHERE d.itemID IN (i.itemID,a.parentItemID))`, func(row *sqlite3.Stmt) error {
			path = row.ColumnText(0)
			return nil
		}, library, key)
	})
	if err != nil {
		return "", err
	}
	if name, stored := strings.CutPrefix(path, "storage:"); stored {
		if !filepath.IsLocal(name) || strings.ContainsAny(name, `/\:`) || name == "." {
			return "", fmt.Errorf("无效 Zotero 附件路径")
		}
		root, err := os.OpenRoot(filepath.Join(z.diskDir, "storage"))
		if err != nil {
			return "", err
		}
		defer root.Close()
		rel := filepath.Join(key, name)
		info, err := root.Stat(rel)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("Zotero 附件不是普通文件")
		}
		path = filepath.Join(root.Name(), rel)
	}
	return zoteroLocalPDF(path)
}

func (z Zotero) diskAnnotations(ctx context.Context, key string, start int) ([]Annotation, bool, error) {
	var annotations []Annotation
	more := false
	err := z.withDisk(ctx, func(conn *sqlite3.Conn) error {
		library, err := z.diskLibraryID(conn)
		if err != nil {
			return err
		}
		return zoteroDiskQuery(conn, `
			SELECT i.key, substr(a.text,1,2048), substr(a.comment,1,2048), substr(a.pageLabel,1,32), a.type,
			       CASE WHEN json_valid(a.position) THEN json_extract(a.position,'$.pageIndex') END
			FROM items p JOIN itemAttachments pdf ON pdf.itemID=p.itemID
			JOIN itemAnnotations a ON a.parentItemID=p.itemID
			JOIN items i ON i.itemID=a.itemID AND i.libraryID=p.libraryID
			WHERE p.libraryID=? AND p.key=? AND pdf.contentType='application/pdf'
			  AND NOT EXISTS (SELECT 1 FROM deletedItems d WHERE d.itemID IN (p.itemID,pdf.parentItemID,i.itemID))
			ORDER BY a.sortIndex,i.itemID LIMIT ? OFFSET ?`, func(row *sqlite3.Stmt) error {
			if len(annotations) == ZoteroPageSize {
				more = true
				return nil
			}
			kind, page := "", 0
			kinds := [...]string{"", "highlight", "note", "image", "ink", "underline", "text"}
			if n := row.ColumnInt(4); n > 0 && n < len(kinds) {
				kind = kinds[n]
			}
			if n := row.ColumnInt(5); row.ColumnType(5) == sqlite3.INTEGER && n >= 0 && n < 1<<30 {
				page = n + 1
			}
			annotations = append(annotations, Annotation{Key: row.ColumnText(0), Quote: cleanField(row.ColumnText(1), 2048),
				Note: cleanField(row.ColumnText(2), 2048), Label: cleanField(row.ColumnText(3), 32), Kind: kind, Page: page})
			return nil
		}, library, key, ZoteroPageSize+1, start)
	})
	return annotations, more, err
}
