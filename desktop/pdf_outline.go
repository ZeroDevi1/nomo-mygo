package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// 文档书签：导出 HTML 在每个标题处放置指向 https://nomo-pdf-outline.invalid/<id> 的定位链接，
// WebView 打印后这些链接成为页面上的 Link 注解。这里读出每个标记所在页与纵坐标，删除标记注解，
// 再按标题层级生成 /Outlines 书签树。

const outlineMarkerPrefix = "https://nomo-pdf-outline.invalid/"

func init() {
	// pdfcpu 默认会在用户配置目录创建自己的配置文件；导出只做内存处理，禁用该副作用。
	model.ConfigPath = "disable"
}

// validatePDF 要求数据以 %PDF- 开头且可解析出至少一页。
func validatePDF(data []byte) error {
	if len(data) < 5 || !bytes.HasPrefix(data, []byte("%PDF-")) {
		return errors.New("文件缺少有效的 PDF 文件头")
	}
	ctx, err := readPDF(data)
	if err != nil {
		return fmt.Errorf("解析 PDF 失败：%w", err)
	}
	if ctx.PageCount == 0 {
		return errors.New("PDF 不包含页面")
	}
	return nil
}

func readPDF(data []byte) (*model.Context, error) {
	ctx, err := api.ReadContext(context.Background(), bytes.NewReader(data), model.NewDefaultConfiguration())
	if err != nil {
		return nil, err
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, err
	}
	return ctx, nil
}

type markerPosition struct {
	page *types.IndirectRef
	top  float64
}

// addDocumentOutline 返回带书签的新 PDF 数据；任何标记缺失或重复都视为失败，由调用方保留原始 PDF。
func addDocumentOutline(data []byte, entries []pdfOutlineEntry) ([]byte, error) {
	if len(entries) == 0 {
		return nil, errors.New("没有可生成的文档标题")
	}
	if err := validateOutlineEntries(entries); err != nil {
		return nil, err
	}
	expected := map[string]bool{}
	for _, entry := range entries {
		expected[entry.MarkerURI] = true
	}
	ctx, err := readPDF(data)
	if err != nil {
		return nil, fmt.Errorf("解析 PDF 失败：%w", err)
	}
	positions, err := collectMarkerPositions(ctx, expected)
	if err != nil {
		return nil, err
	}
	if len(positions) != len(entries) {
		return nil, fmt.Errorf("标题定位标记数量不匹配：expected=%d actual=%d", len(entries), len(positions))
	}
	if err := buildOutlineTree(ctx, entries, positions); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := api.WriteContext(context.Background(), ctx, &output); err != nil {
		return nil, fmt.Errorf("写入带书签 PDF 失败：%w", err)
	}
	if err := validatePDF(output.Bytes()); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func validateOutlineEntries(entries []pdfOutlineEntry) error {
	markers := map[string]bool{}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Title) == "" {
			return errors.New("文档标题不能为空")
		}
		if entry.Level < 1 || entry.Level > 6 {
			return fmt.Errorf("无效的标题级别：%d", entry.Level)
		}
		if !strings.HasPrefix(entry.MarkerURI, outlineMarkerPrefix) {
			return errors.New("文档标题包含无效的定位标记")
		}
		if markers[entry.MarkerURI] {
			return errors.New("文档标题定位标记重复")
		}
		markers[entry.MarkerURI] = true
	}
	return nil
}

// collectMarkerPositions 遍历每页注解，记录预期标记的位置并把它们从页面注解中移除。
func collectMarkerPositions(ctx *model.Context, expected map[string]bool) (map[string]markerPosition, error) {
	xref := ctx.XRefTable
	positions := map[string]markerPosition{}
	for pageNr := 1; pageNr <= ctx.PageCount; pageNr++ {
		page, pageRef, _, err := xref.PageDict(context.Background(), pageNr, false)
		if err != nil {
			return nil, fmt.Errorf("读取 PDF 页面失败：%w", err)
		}
		annotsObject, ok := page.Find("Annots")
		if !ok {
			continue
		}
		annots, err := xref.DereferenceArray(annotsObject)
		if err != nil {
			return nil, fmt.Errorf("PDF 页面注解格式无效：%w", err)
		}
		filtered := make(types.Array, 0, len(annots))
		changed := false
		for _, annotation := range annots {
			uri, top, ok, err := markerFromAnnotation(xref, annotation)
			if err != nil {
				return nil, err
			}
			if ok && expected[uri] {
				if _, duplicate := positions[uri]; duplicate {
					return nil, errors.New("同一个标题定位标记在 PDF 中出现多次")
				}
				positions[uri] = markerPosition{page: pageRef, top: top}
				changed = true
				continue
			}
			filtered = append(filtered, annotation)
		}
		if !changed {
			continue
		}
		if ref, indirect := annotsObject.(types.IndirectRef); indirect {
			if entry, found := xref.FindTableEntryForIndRef(&ref); found {
				entry.Object = filtered
				continue
			}
		}
		if len(filtered) == 0 {
			page.Delete("Annots")
		} else {
			page.Update("Annots", filtered)
		}
	}
	return positions, nil
}

// markerFromAnnotation 识别 URI 指向定位前缀的 Link 注解，返回 URI 与矩形上边缘的纵坐标。
// macOS WebKit 会把 URI 写成间接对象，需要先解引用。
func markerFromAnnotation(xref *model.XRefTable, annotation types.Object) (string, float64, bool, error) {
	dict, err := xref.DereferenceDict(annotation)
	if err != nil || dict == nil {
		return "", 0, false, nil
	}
	if subtype := dict.NameEntry("Subtype"); subtype == nil || *subtype != "Link" {
		return "", 0, false, nil
	}
	actionObject, ok := dict.Find("A")
	if !ok {
		return "", 0, false, nil
	}
	action, err := xref.DereferenceDict(actionObject)
	if err != nil || action == nil {
		return "", 0, false, nil
	}
	if kind := action.NameEntry("S"); kind == nil || *kind != "URI" {
		return "", 0, false, nil
	}
	uriObject, ok := action.Find("URI")
	if !ok {
		return "", 0, false, nil
	}
	uriObject, err = xref.Dereference(uriObject)
	if err != nil {
		return "", 0, false, fmt.Errorf("读取 PDF 链接地址失败：%w", err)
	}
	var uri string
	switch value := uriObject.(type) {
	case types.StringLiteral:
		uri, err = types.StringLiteralToString(value)
	case types.HexLiteral:
		uri, err = types.HexLiteralToString(value)
	default:
		return "", 0, false, nil
	}
	if err != nil || !strings.HasPrefix(uri, outlineMarkerPrefix) {
		return "", 0, false, nil
	}
	rectObject, ok := dict.Find("Rect")
	if !ok {
		return "", 0, false, errors.New("PDF 标题定位区域无效")
	}
	rect, err := xref.DereferenceArray(rectObject)
	if err != nil || len(rect) != 4 {
		return "", 0, false, errors.New("PDF 标题定位区域长度无效")
	}
	y1, ok1 := pdfNumber(xref, rect[1])
	y2, ok2 := pdfNumber(xref, rect[3])
	if !ok1 || !ok2 {
		return "", 0, false, errors.New("PDF 标题定位坐标无效")
	}
	return uri, max(y1, y2), true, nil
}

func pdfNumber(xref *model.XRefTable, object types.Object) (float64, bool) {
	object, err := xref.Dereference(object)
	if err != nil {
		return 0, false
	}
	switch value := object.(type) {
	case types.Integer:
		return float64(value), true
	case types.Float:
		return float64(value), true
	}
	return 0, false
}

type outlineNode struct {
	parent   int // -1 表示顶层
	children []int
	position markerPosition
}

// buildOutlineTree 按标题层级（栈）构造书签树，每项跳转到标记所在页的标记顶部（XYZ）。
func buildOutlineTree(ctx *model.Context, entries []pdfOutlineEntry, positions map[string]markerPosition) error {
	xref := ctx.XRefTable
	nodes := make([]outlineNode, len(entries))
	type stackItem struct {
		level uint8
		index int
	}
	var stack []stackItem
	var roots []int
	for index, entry := range entries {
		for len(stack) > 0 && stack[len(stack)-1].level >= entry.Level {
			stack = stack[:len(stack)-1]
		}
		parent := -1
		if len(stack) > 0 {
			parent = stack[len(stack)-1].index
		}
		position, ok := positions[entry.MarkerURI]
		if !ok {
			return errors.New("缺少 PDF 标题定位信息")
		}
		nodes[index] = outlineNode{parent: parent, position: position}
		if parent >= 0 {
			nodes[parent].children = append(nodes[parent].children, index)
		} else {
			roots = append(roots, index)
		}
		stack = append(stack, stackItem{entry.Level, index})
	}
	// 先为每个节点分配对象号，再填写相互引用。
	rootDict := types.Dict{"Type": types.Name("Outlines")}
	rootRef, err := xref.IndRefForNewObject(rootDict)
	if err != nil {
		return err
	}
	dicts := make([]types.Dict, len(entries))
	refs := make([]*types.IndirectRef, len(entries))
	for index := range entries {
		dicts[index] = types.Dict{}
		if refs[index], err = xref.IndRefForNewObject(dicts[index]); err != nil {
			return err
		}
	}
	for index, entry := range entries {
		node := nodes[index]
		siblings := roots
		parentRef := rootRef
		if node.parent >= 0 {
			siblings = nodes[node.parent].children
			parentRef = refs[node.parent]
		}
		position := -1
		for i, sibling := range siblings {
			if sibling == index {
				position = i
			}
		}
		if position < 0 {
			return errors.New("PDF 书签层级关系无效")
		}
		dict := dicts[index]
		// 标题以 UTF-16BE（带 BOM）的十六进制串写入，无需处理括号与反斜杠转义。
		dict.Insert("Title", types.NewHexLiteral([]byte(types.EncodeUTF16String(entry.Title))))
		dict.Insert("Parent", *parentRef)
		dict.Insert("Dest", types.Array{*node.position.page, types.Name("XYZ"), nil, types.Float(node.position.top), nil})
		if position > 0 {
			dict.Insert("Prev", *refs[siblings[position-1]])
		}
		if position+1 < len(siblings) {
			dict.Insert("Next", *refs[siblings[position+1]])
		}
		if len(node.children) > 0 {
			dict.Insert("First", *refs[node.children[0]])
			dict.Insert("Last", *refs[node.children[len(node.children)-1]])
			dict.Insert("Count", types.Integer(descendantCount(index, nodes)))
		}
	}
	rootDict.Insert("First", *refs[roots[0]])
	rootDict.Insert("Last", *refs[roots[len(roots)-1]])
	rootDict.Insert("Count", types.Integer(len(entries)))
	catalog, err := xref.Catalog()
	if err != nil {
		return fmt.Errorf("读取 PDF 文档目录失败：%w", err)
	}
	catalog.Update("Outlines", *rootRef)
	return nil
}

func descendantCount(index int, nodes []outlineNode) int {
	count := 0
	for _, child := range nodes[index].children {
		count += 1 + descendantCount(child, nodes)
	}
	return count
}
