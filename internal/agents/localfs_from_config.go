package agents

// LocalFSFromRuntime builds a LocalFSConfig for NewLocalTools / NewTrustedLocalTools.
// trustedRoot is the validated absolute workspace boundary; empty means no confinement.
func LocalFSFromRuntime(root, trustedRoot string, autoMode bool) LocalFSConfig {
	return LocalFSConfig{
		Root:        root,
		TrustedRoot: trustedRoot,
		AutoMode:    autoMode,
	}
}
