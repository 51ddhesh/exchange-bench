package compiler

// Language describes how to compile (or stage) a single-file submission and
// how to run the resulting artifact inside the sandbox container.
type Language struct {
	Name       string
	Extensions []string

	// CompileCmd returns the command args for the exchange-bench-compiler
	// container entrypoint. srcFile is the source path inside the container
	// (/src/source.ext); outFile is the artifact destination (/out/binary).
	// nil means the language is interpreted — source is staged directly
	// without invoking Docker.
	CompileCmd func(srcFile, outFile string) []string

	// RunCmd returns the entrypoint command for the sandbox (run) container.
	// artifactPath is the container-internal path to the compiled artifact.
	RunCmd func(artifactPath string) []string
}

// languages is the authoritative registry of supported submission languages.
var languages = map[string]Language{
	"cpp": {
		Name:       "cpp",
		Extensions: []string{".cpp", ".cc", ".cxx"},
		CompileCmd: func(srcFile, outFile string) []string {
			return []string{
				"g++", "-O2", "-std=c++20",
				"-I/usr/local/include",
				"-o", outFile, srcFile,
				"-lssl", "-lcrypto",
			}
		},
		RunCmd: func(artifactPath string) []string {
			return []string{artifactPath}
		},
	},
	"rust": {
		Name:       "rust",
		Extensions: []string{".rs"},
		CompileCmd: func(srcFile, outFile string) []string {
			return []string{"rustc", "-O", "-o", outFile, srcFile}
		},
		RunCmd: func(artifactPath string) []string {
			return []string{artifactPath}
		},
	},
	"go": {
		Name:       "go",
		Extensions: []string{".go"},
		CompileCmd: func(srcFile, outFile string) []string {
			return []string{"go", "build", "-o", outFile, srcFile}
		},
		RunCmd: func(artifactPath string) []string {
			return []string{artifactPath}
		},
	},
	"python": {
		Name:       "python",
		Extensions: []string{".py"},
		CompileCmd: nil, // interpreted: source is staged without Docker
		RunCmd: func(artifactPath string) []string {
			return []string{"python3", artifactPath}
		},
	},
	// "zig": {
	// 	Name:       "zig",
	// 	Extensions: []string{".zig"},
	// 	CompileCmd: func(srcFile, outFile string) []string {
	// 		return []string{
	// 			"zig", "build-exe",
	// 			"-O", "ReleaseFast",
	// 			"-femit-bin=" + outFile,
	// 			srcFile,
	// 		}
	// 	},
	// 	RunCmd: func(artifactPath string) []string {
	// 		return []string{artifactPath}
	// 	},
	// },
}

// Lookup returns the Language for the given identifier.
// Returns the zero Language and false if the language is unsupported.
func Lookup(language string) (Language, bool) {
	l, ok := languages[language]
	return l, ok
}
