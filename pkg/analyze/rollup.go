package analyze

import "github.com/dundee/gdu/v5/pkg/fs"

// SmallObjectsName is the name of the synthetic item that aggregates all
// objects collapsed by a rollup threshold. It mirrors the "<smaller objects>"
// bucket produced by ncdu_to_parquet.py.
const SmallObjectsName = "<smaller objects>"

// Rollup returns a copy of the tree rooted at item in which every file or
// directory whose disk usage is below thresholdBytes is collapsed into a
// synthetic "<smaller objects>" file in its nearest surviving ancestor.
//
// Exact totals are preserved: the bucket carries the summed apparent size and
// disk usage of everything it replaces, so each surviving directory's recursive
// size/usage is unchanged. A sub-threshold directory collapses together with its
// whole subtree, which is always safe because a child's recursive usage can
// never exceed its parent's.
//
// A thresholdBytes of 0 or less disables rollup and returns item unchanged.
// Only *Dir trees are transformed; any other fs.Item is returned as-is.
func Rollup(item fs.Item, thresholdBytes int64) fs.Item {
	if thresholdBytes <= 0 {
		return item
	}
	dir, ok := item.(*Dir)
	if !ok {
		return item
	}
	return rollupDir(dir, thresholdBytes, nil)
}

// rollupDir builds a thresholded copy of src parented to parent.
func rollupDir(src *Dir, threshold int64, parent fs.Item) *Dir {
	dst := &Dir{
		File: &File{
			Name:   src.Name,
			Flag:   src.Flag,
			Size:   src.Size,
			Usage:  src.Usage,
			Mtime:  src.Mtime,
			Mli:    src.Mli,
			Parent: parent,
		},
		BasePath:  src.BasePath,
		ItemCount: src.ItemCount,
		Files:     make(fs.Files, 0, len(src.Files)),
	}

	var bucketSize, bucketUsage, bucketItems int64

	for _, child := range src.Files {
		if child.GetUsage() >= threshold {
			switch c := child.(type) {
			case *Dir:
				dst.AddFile(rollupDir(c, threshold, dst))
			case *File:
				cp := *c
				cp.Parent = dst
				dst.AddFile(&cp)
			default:
				// Unknown item type (e.g. an archive dir): keep it as-is.
				c.SetParent(dst)
				dst.AddFile(c)
			}
			continue
		}
		// Sub-threshold: collapse the item (and any subtree) into the bucket.
		bucketSize += child.GetSize()
		bucketUsage += child.GetUsage()
		bucketItems += child.GetItemCount()
	}

	if bucketItems > 0 {
		dst.AddFile(&File{
			Name:   SmallObjectsName,
			Size:   bucketSize,
			Usage:  bucketUsage,
			Flag:   ' ',
			Parent: dst,
		})
	}

	return dst
}
