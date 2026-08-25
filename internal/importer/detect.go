package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"svc/internal/apperr"
	"svc/internal/helpers"
	"svc/internal/sdk"
)

// detectVersionFromDir runs the fetcher's verify command against an SDK
// directory to discover its version (layer-1 import pre-check). Searches the
// declared binDirs, then sdkRoot/bin, then sdkRoot itself.
func (s *Service) detectVersionFromDir(sdkRoot string, f sdk.VersionFetcher) (string, error) {
	cmdName, args := f.VerifyCommand()
	sdkType := string(f.Type())

	// Search each declared binDir for the executable. SDKs like Go
	// (go/bin), Dart (dart-sdk/bin), Android (cmdline-tools/bin) ship
	// binaries in wrapper subdirs that a plain sdkRoot/bin/ check misses.
	var binPath, binDir string
	for _, bd := range f.GetBinDirs() {
		dir := sdkRoot
		if bd != "" {
			dir = filepath.Join(sdkRoot, bd)
		}
		if p := helpers.FindExecutable(dir, cmdName); p != "" {
			binPath = p
			binDir = dir
			break
		}
	}
	// Fallback: try sdkRoot/bin/ (common layout for stripped SDKs)
	if binPath == "" {
		if d := filepath.Join(sdkRoot, "bin"); helpers.IsDir(d) {
			if p := helpers.FindExecutable(d, cmdName); p != "" {
				binPath = p
				binDir = d
			}
		}
	}
	// Final fallback: sdkRoot itself (SDKs with binDir = "")
	if binPath == "" {
		binDir = sdkRoot
		binPath = helpers.FindExecutable(binDir, cmdName)
	}
	if binPath == "" {
		return "", apperr.New(apperr.ExecNotFound, map[string]string{"cmd": cmdName})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := helpers.CreateCmdContext(ctx, binPath, args...)

	sysPath := sdk.GetSystemPath()
	extraPath := binDir
	if sysPath != "" {
		extraPath = binDir + string(os.PathListSeparator) + sysPath
	}
	env := helpers.ReplacePathEnv(os.Environ(), extraPath)

	if sdkType == "maven" || sdkType == "gradle" {
		javaHome := s.findJavaHome()
		if javaHome == "" {
			return "", apperr.New(apperr.JdkRequired, map[string]string{"sdk": sdkType})
		}
		env = append(env, "JAVA_HOME="+javaHome)
	}

	if sdkType == "android" {
		javaHome := s.findJavaHome()
		if javaHome != "" {
			env = append(env, "JAVA_HOME="+javaHome)
		}
	}

	c.Env = env
	out, err := c.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", apperr.New(apperr.ExecTimeout, map[string]string{"cmd": cmdName})
		}
		return "", apperr.New(apperr.ExecFailed, map[string]string{"cmd": cmdName, "detail": strings.TrimSpace(string(out))})
	}

	ver := helpers.ExtractVersionFromString(string(out))
	if ver == "" {
		return "", apperr.New(apperr.VersionParseFail, map[string]string{"cmd": cmdName})
	}
	return ver, nil
}

// findJavaHome locates a usable JAVA_HOME for maven/gradle/android imports:
// the app-managed active JDK, any app-managed JDK version, or the JAVA_HOME
// environment variable.
func (s *Service) findJavaHome() string {
	jdkDir := s.cfg.SdkDir("jdk")
	activeVersion := s.cfg.GetActiveVersion("jdk")
	if activeVersion != "" {
		jdkRoot := filepath.Join(jdkDir, activeVersion)
		if helpers.IsDir(jdkRoot) {
			return jdkRoot
		}
	}
	if entries, err := os.ReadDir(jdkDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				return filepath.Join(jdkDir, e.Name())
			}
		}
	}
	if jh := os.Getenv("JAVA_HOME"); jh != "" && helpers.IsDir(jh) {
		return jh
	}
	return ""
}

// postImportVerifier returns the layer-2 post-check shared by all three
// import flows: after the layout is aligned and the tree is copied, the
// destination must still report exactly the version detected pre-copy.
func (s *Service) postImportVerifier(f sdk.VersionFetcher, versionName string) func(string) error {
	return func(dir string) error {
		postVer, err := s.detectVersionFromDir(dir, f)
		if err != nil {
			// Leaf error carries the user-facing reason — pass through unwrapped.
			return err
		}
		if postVer != versionName {
			return apperr.New(apperr.VersionMismatch, map[string]string{"expected": versionName, "got": postVer})
		}
		return nil
	}
}
