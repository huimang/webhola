package capture

import (
	"os"
	"path/filepath"

	i18npkg "webhola/native/internal/i18n"
	platformpkg "webhola/native/internal/platform"
)

func ConfiguredTShark(path string) (string, error) {
	path = platformpkg.CleanTSharkPath(path)
	if path == "" {
		return FindTShark()
	}
	if !filepath.IsAbs(path) {
		return "", i18npkg.MessageError("settings.select_the_full_path_to_tshark_exe")
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", i18npkg.MessageError("settings.tshark_path_is_unavailable_w", err)
	}
	if st.IsDir() {
		return "", i18npkg.MessageError("settings.select_the_tshark_exe_file_instead_of")
	}
	return path, nil
}
