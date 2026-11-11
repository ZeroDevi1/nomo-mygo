package scholar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
)

type zoteroHTTPError int

func (e zoteroHTTPError) Error() string {
	return fmt.Sprintf("Zotero 本地接口返回 HTTP %d", e)
}

// Only authorizations Zotero marks reusable are kept, in memory for this connection.
type zoteroWriteKey struct{ atomic.Pointer[string] }

func (z Zotero) write(ctx context.Context, method, path string, value any, token string) ([]byte, error) {
	if z.ReadOnly() {
		return nil, errZoteroReadOnly
	}
	if !z.CanWrite() {
		return nil, fmt.Errorf("该 Zotero 版本没有可用的本地写接口")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(payload) > 1<<20 {
		return nil, fmt.Errorf("Zotero 写入内容超过 1 MiB")
	}
	key := z.writeKey.Load()
	if key != nil {
		_, raw, err := z.requestWithToken(ctx, method, path, payload, *key, token)
		if !errors.Is(err, zoteroHTTPError(http.StatusUnauthorized)) {
			return raw, zoteroWriteError(err)
		}
		z.writeKey.CompareAndSwap(key, nil)
	}
	_, raw, err := z.request(ctx, http.MethodPost, "/api/local/authorize", []byte(`{"appName":"Nomo Scholar"}`), "")
	if errors.Is(err, zoteroHTTPError(http.StatusForbidden)) {
		return nil, fmt.Errorf("Zotero 写入授权已拒绝")
	}
	if err != nil {
		return nil, zoteroWriteError(err)
	}
	var auth struct {
		Key      string
		Remember bool
	}
	if err := json.Unmarshal(raw, &auth); err != nil || auth.Key == "" {
		return nil, fmt.Errorf("未获得 Zotero 本地写入授权")
	}
	if auth.Remember {
		z.writeKey.Store(&auth.Key)
	}
	_, raw, err = z.requestWithToken(ctx, method, path, payload, auth.Key, token)
	return raw, zoteroWriteError(err)
}

func zoteroWriteError(err error) error {
	switch err {
	case zoteroHTTPError(http.StatusForbidden):
		return fmt.Errorf("当前 Zotero 文献库不允许此写入")
	case zoteroHTTPError(http.StatusPreconditionFailed):
		return fmt.Errorf("Zotero 数据或连接已变化，请刷新文献库后核对，再重新操作")
	case zoteroHTTPError(http.StatusTooManyRequests):
		return fmt.Errorf("Zotero 授权请求过于频繁，请稍后重试")
	}
	return err
}

func (z Zotero) createObject(ctx context.Context, path string, value any, token string) error {
	raw, err := z.write(ctx, http.MethodPost, path, []any{value}, token)
	if err != nil {
		return err
	}
	var result struct {
		Successful map[string]json.RawMessage
		Failed     map[string]struct{ Code int }
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if failure, ok := result.Failed["0"]; ok {
		return zoteroWriteError(zoteroHTTPError(failure.Code))
	}
	if _, ok := result.Successful["0"]; !ok {
		return fmt.Errorf("Zotero 未确认创建")
	}
	return nil
}

func (z Zotero) patchObject(ctx context.Context, path string, version *int, patch map[string]any) error {
	if version == nil {
		return fmt.Errorf("未读取到 Zotero 条目版本，请刷新文献库")
	}
	patch["version"] = *version
	_, err := z.write(ctx, http.MethodPatch, path, patch, "")
	return err
}

type zoteroTag struct {
	Tag  string `json:"tag"`
	Type int    `json:"type,omitempty"`
}

type ZoteroCreator struct {
	Type       string `json:"creatorType"`
	FirstName  string `json:"firstName,omitempty"`
	LastName   string `json:"lastName,omitempty"`
	Name       string `json:"name,omitempty"`
	SingleName bool   `json:"-"`
}

type ZoteroItemType struct{ ItemType, Localized string }
type ZoteroCreatorType struct{ CreatorType, Localized string }

type ZoteroEditField struct {
	Label, Value string
	key, initial string
}

type ZoteroItemDraft struct {
	Fields       []ZoteroEditField
	Creators     []ZoteroCreator
	CreatorTypes []ZoteroCreatorType
	Tags         []string

	z                Zotero
	key, kind        string
	version          *int
	originalTags     []zoteroTag
	originalCreators []ZoteroCreator
}

func (z Zotero) ItemTypes(ctx context.Context) ([]ZoteroItemType, error) {
	var types []ZoteroItemType
	_, raw, err := z.request(ctx, http.MethodGet, "/api/itemTypes", nil, "")
	if err == nil {
		err = json.Unmarshal(raw, &types)
	}
	return slices.DeleteFunc(types, func(t ZoteroItemType) bool { return !zoteroRegularItem(t.ItemType) }), err
}

func (z Zotero) itemFields(ctx context.Context, kind string) ([]struct{ Field, Localized string }, error) {
	var fields []struct{ Field, Localized string }
	_, raw, err := z.request(ctx, http.MethodGet, "/api/itemTypeFields?itemType="+url.QueryEscape(kind), nil, "")
	if err == nil {
		err = json.Unmarshal(raw, &fields)
	}
	return fields, err
}

func zoteroRegularItem(kind string) bool {
	return kind != "" && kind != "note" && kind != "attachment" && kind != "annotation"
}

func (z Zotero) itemData(ctx context.Context, key string, dest any) error {
	if !zoteroKey.MatchString(key) {
		return fmt.Errorf("无效 Zotero 条目键")
	}
	return z.objectData(ctx, "/api/users/0/items/"+key, key, dest)
}

func (z Zotero) objectData(ctx context.Context, path, key string, dest any) error {
	_, raw, err := z.request(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return err
	}
	var object struct {
		Key  string
		Data json.RawMessage
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	if object.Key != key || len(object.Data) == 0 || string(object.Data) == "null" {
		return fmt.Errorf("Zotero 返回的对象与请求不符")
	}
	return json.Unmarshal(object.Data, dest)
}

func (z Zotero) ItemDraft(ctx context.Context, key, kind string) (*ZoteroItemDraft, error) {
	draft := &ZoteroItemDraft{z: z, key: key, kind: kind}
	var values map[string]json.RawMessage
	if key != "" {
		var item struct {
			Version  *int
			ItemType string
			Deleted  bool
			Tags     []zoteroTag
			Creators []ZoteroCreator
		}
		var raw json.RawMessage
		if err := z.itemData(ctx, key, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		if item.Deleted {
			return nil, fmt.Errorf("请先恢复此 Zotero 条目")
		}
		draft.kind, draft.version = item.ItemType, item.Version
		for i := range item.Creators {
			item.Creators[i].SingleName = item.Creators[i].Name != ""
		}
		draft.originalTags, draft.originalCreators = item.Tags, item.Creators
		draft.Creators = slices.Clone(item.Creators)
		for _, tag := range item.Tags {
			draft.Tags = append(draft.Tags, tag.Tag)
		}
	}
	if !zoteroRegularItem(draft.kind) {
		return nil, fmt.Errorf("此 Zotero 条目类型不支持编辑元数据")
	}
	fields, err := z.itemFields(ctx, draft.kind)
	if err != nil {
		return nil, err
	}
	for _, field := range fields {
		var initial string
		if raw := values[field.Field]; raw != nil {
			if err := json.Unmarshal(raw, &initial); err != nil {
				return nil, err
			}
		}
		draft.Fields = append(draft.Fields, ZoteroEditField{field.Localized, initial, field.Field, initial})
	}
	_, raw, err := z.request(ctx, http.MethodGet, "/api/itemTypeCreatorTypes?itemType="+url.QueryEscape(draft.kind), nil, "")
	if err == nil {
		err = json.Unmarshal(raw, &draft.CreatorTypes)
	}
	return draft, err
}

func (f ZoteroEditField) Multiline() bool { return f.key == "abstractNote" || f.key == "extra" }

func (d *ZoteroItemDraft) Save(ctx context.Context, collection, token string) error {
	patch := make(map[string]any)
	for _, field := range d.Fields {
		switch field.key {
		case "title", "caseName", "subject", "nameOfAct":
			if (d.key == "" || field.Value != field.initial) && strings.TrimSpace(field.Value) == "" {
				return fmt.Errorf("请输入 Zotero 条目标题")
			}
		}
		if field.Value != field.initial {
			patch[field.key] = field.Value
		}
	}
	if !slices.Equal(d.Creators, d.originalCreators) {
		creators := make([]ZoteroCreator, 0, len(d.Creators))
		for _, creator := range d.Creators {
			if creator.SingleName {
				creator.FirstName, creator.LastName = "", ""
			} else {
				creator.Name = ""
			}
			if strings.TrimSpace(creator.Name+creator.FirstName+creator.LastName) == "" {
				return fmt.Errorf("请填写作者姓名或移除空白行")
			}
			creators = append(creators, creator)
		}
		patch["creators"] = creators
	}
	var originals []string
	for _, tag := range d.originalTags {
		originals = append(originals, tag.Tag)
	}
	if !slices.Equal(d.Tags, originals) {
		tags := make([]zoteroTag, 0, len(d.Tags))
		for _, name := range d.Tags {
			tag := zoteroTag{Tag: name}
			if i := slices.IndexFunc(d.originalTags, func(t zoteroTag) bool { return t.Tag == name }); i >= 0 {
				tag = d.originalTags[i]
			}
			tags = append(tags, tag)
		}
		patch["tags"] = tags
	}
	if d.key != "" {
		if len(patch) == 0 {
			return nil
		}
		return d.z.patchObject(ctx, "/api/users/0/items/"+d.key, d.version, patch)
	}
	patch["itemType"] = d.kind
	if collection != "" {
		if !zoteroKey.MatchString(collection) {
			return fmt.Errorf("无效 Zotero 分类键")
		}
		patch["collections"] = []string{collection}
	}
	return d.z.createObject(ctx, "/api/users/0/items", patch, token)
}

func (z Zotero) CreateNote(ctx context.Context, parent, text, token string) error {
	text = strings.TrimSpace(text)
	if text == "" || !zoteroKey.MatchString(parent) {
		return fmt.Errorf("需要有效的父条目和笔记正文")
	}
	body := "<p>" + strings.ReplaceAll(html.EscapeString(strings.ReplaceAll(text, "\r\n", "\n")), "\n", "<br>") + "</p>"
	return z.createObject(ctx, "/api/users/0/items", map[string]any{"itemType": "note", "parentItem": parent, "note": body}, token)
}

func (z Zotero) SetItemTrashed(ctx context.Context, key string, deleted bool) error {
	var item struct {
		Version *int
		Deleted bool
	}
	if err := z.itemData(ctx, key, &item); err != nil || item.Deleted == deleted {
		return err
	}
	return z.patchObject(ctx, "/api/users/0/items/"+key, item.Version, map[string]any{"deleted": deleted})
}

func (z Zotero) SetItemCollection(ctx context.Context, key, collection string, add bool) error {
	if !zoteroKey.MatchString(collection) {
		return fmt.Errorf("无效 Zotero 分类键")
	}
	var item struct {
		Version     *int
		Deleted     bool
		ParentItem  json.RawMessage
		Collections []string
	}
	if err := z.itemData(ctx, key, &item); err != nil {
		return err
	}
	var parent string
	_ = json.Unmarshal(item.ParentItem, &parent)
	if item.Deleted || parent != "" {
		return fmt.Errorf("只能调整未删除的顶层 Zotero 条目的分类")
	}
	index := slices.Index(item.Collections, collection)
	switch {
	case add && index < 0:
		item.Collections = append(item.Collections, collection)
	case !add && index >= 0:
		item.Collections = slices.Delete(item.Collections, index, index+1)
	default:
		return nil
	}
	if item.Collections == nil {
		item.Collections = []string{}
	}
	return z.patchObject(ctx, "/api/users/0/items/"+key, item.Version, map[string]any{"collections": item.Collections})
}

type ZoteroCollectionDraft struct {
	Name    string
	version *int
}

func (z Zotero) CollectionDraft(ctx context.Context, key string) (ZoteroCollectionDraft, error) {
	var draft ZoteroCollectionDraft
	if !zoteroKey.MatchString(key) {
		return draft, fmt.Errorf("无效 Zotero 分类键")
	}
	var data struct {
		Name    string
		Version *int
	}
	err := z.objectData(ctx, "/api/users/0/collections/"+key, key, &data)
	return ZoteroCollectionDraft{data.Name, data.Version}, err
}

func (z Zotero) SaveCollection(ctx context.Context, key, parent string, draft ZoteroCollectionDraft, token string) error {
	name := strings.TrimSpace(draft.Name)
	if name == "" {
		return fmt.Errorf("请输入 Zotero 分类名称")
	}
	value := map[string]any{"name": name}
	if key != "" {
		if !zoteroKey.MatchString(key) {
			return fmt.Errorf("无效 Zotero 分类键")
		}
		return z.patchObject(ctx, "/api/users/0/collections/"+key, draft.version, value)
	}
	if parent != "" {
		if !zoteroKey.MatchString(parent) {
			return fmt.Errorf("无效 Zotero 分类键")
		}
		value["parentCollection"] = parent
	}
	return z.createObject(ctx, "/api/users/0/collections", value, token)
}
