package textdoc

import "unicode/utf8"

// readWindowFromSnapshot 读取对齐后的窗口：起止点不落在 UTF-8 续字节或 CRLF 中间，
// 并补齐起始行号、长行标记与 JSON 词法状态。lossy 模式（未完成校验或非 UTF-8）不做字符对齐。
func readWindowFromSnapshot(s *session, snap *snapshot, index *lineIndex, requestedStart uint64, targetBytes int, requestID *uint64) (*Window, error) {
	if targetBytes <= 0 || targetBytes > maxWindowBytes {
		return nil, newErrorf("invalid-window-size", "窗口大小必须在 1..=%d 字节之间", maxWindowBytes)
	}
	meta := s.metadata()
	lossy := !meta.validationComplete || meta.encoding == EncodingUnsupported
	length := snap.len()
	isContinuation := func(offset uint64) (bool, error) {
		b, ok, err := snap.byteAt(offset)
		return ok && b&0b1100_0000 == 0b1000_0000, err
	}
	start := min(requestedStart, length)
	if !lossy {
		for range 3 {
			if start == 0 {
				break
			}
			continuation, err := isContinuation(start)
			if err != nil {
				return nil, err
			}
			if !continuation {
				break
			}
			start--
		}
	}
	if start > 0 {
		inside, err := isInsideCRLF(snap, start)
		if err != nil {
			return nil, err
		}
		if inside {
			start--
		}
	}
	end := min(start+uint64(max(targetBytes, 1)), length)
	if !lossy {
		for range 3 {
			if end >= length {
				break
			}
			continuation, err := isContinuation(end)
			if err != nil {
				return nil, err
			}
			if !continuation {
				break
			}
			end++
		}
	}
	if end < length && end > 0 {
		inside, err := isInsideCRLF(snap, end)
		if err != nil {
			return nil, err
		}
		if inside {
			end++
		}
	}
	data := s.cache.get(snap.revision, start, end)
	if data == nil {
		read, err := snap.readRange(start, int(end-start))
		if err != nil {
			return nil, err
		}
		data = s.cache.insert(snap.revision, start, end, read)
	}
	if uint64(len(data)) != end-start {
		return nil, newError("unexpected-eof", "读取窗口时正文提前结束")
	}
	window := &Window{RequestID: requestID, Revision: snap.revision, StartByte: start, EndByte: end}
	if lossy {
		window.Text, window.UTF16ByteOffsets = decodeLossyWithUTF16Offsets(data)
	} else {
		if !utf8.Valid(data) {
			return nil, newError("invalid-utf8-window", "窗口包含无效 UTF-8")
		}
		window.Text = string(data)
	}
	startLine, activeIndex, err := lineNumberForOffset(s, snap, index, start)
	if err != nil {
		return nil, err
	}
	window.StartLine = startLine
	lineStart, err := isLineStart(snap, start)
	if err != nil {
		return nil, err
	}
	window.LeadingPartialLine = !lineStart
	if end < length && end > start {
		endLineStart, err := isLineStart(snap, end)
		if err != nil {
			return nil, err
		}
		window.TrailingPartialLine = !endLineStart
	}
	if window.LongLine, err = windowIntersectsLongLine(snap, start, end, data, longLineThresholdBytes); err != nil {
		return nil, err
	}
	if s.kind == KindJSON {
		if checkpoint, state, ok := activeIndex.jsonCheckpoint(start); ok {
			for cursor := checkpoint; cursor < start; {
				chunk, err := snap.readRange(cursor, int(min(start-cursor, 256*1024)))
				if err != nil {
					return nil, err
				}
				if len(chunk) == 0 {
					break
				}
				advanceJSONState(&state, chunk)
				cursor += uint64(len(chunk))
			}
			window.JSONLexicalState = &state
		}
	}
	window.IndexProgress = activeIndex.progress()
	return window, nil
}

// isInsideCRLF 报告 offset 是否正好位于 CR 与 LF 之间。
func isInsideCRLF(snap *snapshot, offset uint64) (bool, error) {
	before, ok, err := snap.byteAt(offset - 1)
	if err != nil || !ok || before != '\r' {
		return false, err
	}
	after, ok, err := snap.byteAt(offset)
	return ok && after == '\n', err
}

// lineNumberForOffset 从最近检查点补扫到 target 求行号，并把扫描得到的检查点回写到
// pending 索引，避免快速滚动反复从远处扫描。返回行号与本次窗口应使用的索引。
func lineNumberForOffset(s *session, snap *snapshot, fallback *lineIndex, target uint64) (uint64, *lineIndex, error) {
	s.lineScanMu.Lock()
	defer s.lineScanMu.Unlock()
	active := fallback
	s.stateMu.RLock()
	if s.state.revision == snap.revision {
		active = s.state.lineIndex
	}
	s.stateMu.RUnlock()
	cursor, lines, previousCR := active.nearestLineCheckpoint(target)
	var checkpoints []scannedCheckpoint
	lastCheckpoint := cursor
	for cursor < target {
		chunk, err := snap.readRange(cursor, int(min(target-cursor, 256*1024)))
		if err != nil {
			return 0, nil, err
		}
		if len(chunk) == 0 {
			break
		}
		lines += advanceLineBreaks(chunk, &previousCR)
		cursor += uint64(len(chunk))
		if cursor-lastCheckpoint >= 1024*1024 || cursor == target {
			checkpoints = append(checkpoints, scannedCheckpoint{byteOffset: cursor, line: lines, previousCR: previousCR})
			lastCheckpoint = cursor
		}
	}
	// target 不会位于 CRLF 中间；前缀尾部 CR 后不是 LF 时，它已构成完整的孤立 CR 换行。
	if previousCR {
		next, ok, err := snap.byteAt(target)
		if err != nil {
			return 0, nil, err
		}
		if !ok || next != '\n' {
			lines++
			if last := len(checkpoints) - 1; last >= 0 && checkpoints[last].byteOffset == target {
				checkpoints[last] = scannedCheckpoint{byteOffset: target, line: lines}
			} else {
				checkpoints = append(checkpoints, scannedCheckpoint{byteOffset: target, line: lines})
			}
		}
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.state.revision != snap.revision {
		return lines, fallback, nil
	}
	if !s.state.lineIndex.Completed && len(checkpoints) > 0 {
		s.state.lineIndex = s.state.lineIndex.withLineCheckpoints(checkpoints)
	}
	return lines, s.state.lineIndex, nil
}

func isLineStart(snap *snapshot, offset uint64) (bool, error) {
	if offset == 0 {
		return true, nil
	}
	before, ok, err := snap.byteAt(offset - 1)
	if err != nil || !ok {
		return false, err
	}
	switch before {
	case '\n':
		return true, nil
	case '\r':
		if offset == snap.len() {
			return true, nil
		}
		after, ok, err := snap.byteAt(offset)
		return !ok || after != '\n', err
	}
	return false, nil
}

// windowIntersectsLongLine 判断窗口是否触及超过阈值的长行（含窗口两端延伸出去的同一行）。
func windowIntersectsLongLine(snap *snapshot, start, end uint64, data []byte, threshold int) (bool, error) {
	var newlines []int
	for i, b := range data {
		if b == '\n' || b == '\r' {
			newlines = append(newlines, i)
		}
	}
	if len(newlines) == 0 {
		if len(data) > threshold {
			return true, nil
		}
		before, err := sameLineBytesBefore(snap, start, threshold+1)
		if err != nil {
			return false, err
		}
		after, err := sameLineBytesAfter(snap, end, threshold+1)
		if err != nil {
			return false, err
		}
		return before+len(data)+after > threshold, nil
	}
	first := newlines[0]
	if first > threshold {
		return true, nil
	}
	before, err := sameLineBytesBefore(snap, start, threshold+1)
	if err != nil {
		return false, err
	}
	if before+first > threshold {
		return true, nil
	}
	for i := 1; i < len(newlines); i++ {
		if newlines[i]-(newlines[i-1]+1) > threshold {
			return true, nil
		}
	}
	trailing := len(data) - (newlines[len(newlines)-1] + 1)
	if trailing > threshold {
		return true, nil
	}
	after, err := sameLineBytesAfter(snap, end, threshold+1)
	if err != nil {
		return false, err
	}
	return trailing+after > threshold, nil
}

// sameLineBytesBefore 统计 start 之前同一行的字节数，最多数到 limit+1 即停。
func sameLineBytesBefore(snap *snapshot, start uint64, limit int) (int, error) {
	cursor, total := start, 0
	for cursor > 0 && total <= limit {
		length := min(int(min(cursor, 64*1024)), limit+1-total)
		chunkStart := cursor - uint64(length)
		chunk, err := snap.readRange(chunkStart, length)
		if err != nil {
			return 0, err
		}
		for i := len(chunk) - 1; i >= 0; i-- {
			if chunk[i] == '\n' || chunk[i] == '\r' {
				return total + len(chunk) - (i + 1), nil
			}
		}
		total += len(chunk)
		cursor = chunkStart
	}
	return total, nil
}

// sameLineBytesAfter 统计 end 之后同一行的字节数，最多数到 limit+1 即停。
func sameLineBytesAfter(snap *snapshot, end uint64, limit int) (int, error) {
	cursor, total := end, 0
	for cursor < snap.len() && total <= limit {
		length := min(int(min(snap.len()-cursor, 64*1024)), limit+1-total)
		chunk, err := snap.readRange(cursor, length)
		if err != nil {
			return 0, err
		}
		for i, b := range chunk {
			if b == '\n' || b == '\r' {
				return total + i, nil
			}
		}
		total += len(chunk)
		cursor += uint64(len(chunk))
	}
	return total, nil
}
