package sftp

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// TransferItem is one leaf of a recursive copy, relative to the root the user
// picked. Directories are listed too: they have to exist before their files.
type TransferItem struct {
	RelPath string // slash-separated, relative to the source root; "" is the root itself
	Size    int64
	IsDir   bool
}

// Plan walks root — a file or a directory — and returns everything to copy plus
// the byte total. The total is what makes the progress bar a percentage, so it
// has to be known before the first byte moves, which is why this is a separate
// step and not something the transfer discovers as it goes.
//
// Symlinks are counted in skipped and never followed: a link that points at its
// own parent would otherwise be an infinite walk. Only Browser.List and
// Browser.Stat are used, so the same code walks the local and the remote side.
func Plan(br Browser, root string) (items []TransferItem, total int64, skipped int, err error) {
	info, ok, err := br.Stat(root)
	if err != nil {
		return nil, 0, 0, err
	}
	if !ok {
		return nil, 0, 0, fmt.Errorf("%s: %w", root, fs.ErrNotExist)
	}
	if info.Mode&fs.ModeSymlink != 0 {
		return nil, 0, 1, nil
	}
	if !info.IsDir {
		return []TransferItem{{Size: info.Size}}, info.Size, 0, nil
	}

	items = []TransferItem{{IsDir: true}}
	// Breadth-first, which gives the ordering the copy needs for free: a
	// directory is always emitted before anything inside it.
	queue := []string{""}
	for len(queue) > 0 {
		rel := queue[0]
		queue = queue[1:]

		entries, listErr := br.List(joinRel(br, root, rel))
		if listErr != nil {
			return nil, 0, 0, listErr
		}
		for _, e := range entries {
			child := e.Name
			if rel != "" {
				child = rel + "/" + e.Name
			}
			switch {
			case e.Mode&fs.ModeSymlink != 0:
				skipped++
			case e.IsDir:
				items = append(items, TransferItem{RelPath: child, IsDir: true})
				queue = append(queue, child)
			default:
				items = append(items, TransferItem{RelPath: child, Size: e.Size})
				total += e.Size
			}
		}
	}
	return items, total, skipped, nil
}

// joinRel walks a slash-separated relative path through the browser's own Join,
// so a local path keeps the host separator and a remote one stays slashed.
func joinRel(br Browser, root, rel string) string {
	p := root
	if rel == "" {
		return p
	}
	for _, seg := range strings.Split(rel, "/") {
		p = br.Join(p, seg)
	}
	return p
}

// Set is one confirmed copy: a root on each side and everything under it.
type Set struct {
	Upload  bool
	SrcRoot string
	DstRoot string
	Items   []TransferItem
	Skipped int
}

// Result is what a finished Set moved.
type Result struct {
	Files   int
	Bytes   int64
	Skipped int
}

// RunSet copies every item of a set, creating directories before their
// contents.
//
// A failure stops the walk and is returned as-is; what has already been copied
// stays where it is. Deleting a half-written tree off a server is more dangerous
// than leaving one behind, so there is deliberately no rollback.
func RunSet(ctx context.Context, r *Remote, set Set, p *Progress) (Result, error) {
	res := Result{Skipped: set.Skipped}

	src, dst := Browser(Local{}), Browser(r)
	if !set.Upload {
		src, dst = Browser(r), Browser(Local{})
	}

	for _, item := range set.Items {
		if err := ctx.Err(); err != nil {
			return res, cancelled(err)
		}
		srcPath := joinRel(src, set.SrcRoot, item.RelPath)
		dstPath := joinRel(dst, set.DstRoot, item.RelPath)

		if item.IsDir {
			if err := mkdirAll(r, set.Upload, dstPath); err != nil {
				return res, err
			}
			continue
		}

		var (
			n   int64
			err error
		)
		if set.Upload {
			n, err = Upload(ctx, r, srcPath, dstPath, p)
		} else {
			n, err = Download(ctx, r, srcPath, dstPath, p)
		}
		res.Bytes += n
		if err != nil {
			return res, err
		}
		res.Files++
	}
	return res, nil
}

// mkdirAll creates a destination directory on whichever side it lives.
// pkg/sftp's MkdirAll is happy with a path that already exists, as is os's.
func mkdirAll(r *Remote, upload bool, dir string) error {
	var err error
	if upload {
		err = r.sc.MkdirAll(dir)
	} else {
		err = os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return nil
}
