package main

// categories.go: a download folder per file type (like IDM): videos can go
// to one folder, music to another, and so on. A category with no folder
// uses the main download folder. When the user picks another folder in the
// "Download File Info" window, BDDM remembers it for that file type.

import (
	"path/filepath"
	"strings"
)

// Category is one file type with its own download folder.
type Category struct {
	Name string `json:"name"` // Video, Audio, Documents, Compressed, Programs, Images, Other
	Exts string `json:"exts"` // extensions without dots, separated by spaces
	Dir  string `json:"dir"`  // "" = the main download folder
}

const otherCategory = "Other"

func defaultCategories() []Category {
	return []Category{
		{Name: "Video", Exts: "mp4 mkv avi mov wmv flv webm m4v mpg mpeg mpe 3gp 3g2 ts m2ts mts ogv vob asf rm rmvb qt"},
		{Name: "Audio", Exts: "mp3 m4a aac wav flac ogg oga opus wma aif aiff ape mka mid midi ra ram amr"},
		{Name: "Documents", Exts: "pdf doc docx xls xlsx ppt pptx pps ppsx odt ods odp rtf txt csv epub mobi azw3 djvu xps md tex"},
		{Name: "Compressed", Exts: "zip rar 7z tar gz tgz bz2 tbz2 xz txz zst lz lzma cab arj ace iso img r00 r01 001 sit sitx"},
		{Name: "Programs", Exts: "exe msi msix msixbundle appx apk xapk dmg pkg deb rpm appimage bat cmd ps1 jar"},
		{Name: "Images", Exts: "jpg jpeg png gif bmp webp svg tif tiff heic heif ico psd raw cr2 nef avif"},
		{Name: otherCategory},
	}
}

// fixCategories keeps the known categories (in order), adds missing ones and
// cleans the extension lists and folders.
func fixCategories(in []Category) []Category {
	out := defaultCategories()
	for i := range out {
		for _, c := range in {
			if strings.EqualFold(c.Name, out[i].Name) {
				if out[i].Name != otherCategory {
					out[i].Exts = cleanExts(c.Exts)
				}
				out[i].Dir = strings.TrimSpace(c.Dir)
			}
		}
	}
	return out
}

func cleanExts(s string) string {
	seen := map[string]bool{}
	var list []string
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return r == ' ' || r == ',' || r == ';' || r == '\t' || r == '\n'
	}) {
		f = strings.TrimPrefix(f, "*")
		f = strings.TrimPrefix(f, ".")
		if f != "" && !seen[f] {
			seen[f] = true
			list = append(list, f)
		}
	}
	return strings.Join(list, " ")
}

// categoryOf returns the category name for a file name.
func (s *Settings) categoryOf(name string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(strings.TrimSpace(name))), ".")
	if ext == "" {
		return otherCategory
	}
	for _, c := range s.Categories {
		for _, e := range strings.Fields(c.Exts) {
			if e == ext {
				return c.Name
			}
		}
	}
	return otherCategory
}

// dirForCategory: the folder of a category (the main folder when not set).
func (s *Settings) dirForCategory(cat string) string {
	for _, c := range s.Categories {
		if c.Name == cat && c.Dir != "" {
			return c.Dir
		}
	}
	return s.DownloadDir
}

// dirFor returns the folder a file of this name goes to.
func (s *Settings) dirFor(name string) string { return s.dirForCategory(s.categoryOf(name)) }

// rememberDirLocked: the user saved a file of category cat in dir, so files
// of that type go there from now on. m.mu must be held. Returns whether
// something changed.
func (m *Manager) rememberDirLocked(cat, dir string) bool {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "." {
		return false
	}
	s := &m.settings
	if filepath.Clean(s.dirForCategory(cat)) == dir {
		return false
	}
	for i := range s.Categories {
		if s.Categories[i].Name == cat {
			if filepath.Clean(s.DownloadDir) == dir {
				s.Categories[i].Dir = "" // the main folder again
			} else {
				s.Categories[i].Dir = dir
			}
			go m.saveSettings()
			return true
		}
	}
	return false
}
