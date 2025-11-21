package intel

import (
	"encoding/json"
	"os"

	dpll_netlink "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
)

var (
	unitTest bool

	// DpllPins allows for mocking DPLL pin info
	DpllPins = []*dpll_netlink.PinInfo{}
)

// MockPins sets up pin mocking for unit testing
func MockPins() {
	pins, err := loadPins("./testdata/dpll-pins.json")
	if err != nil {
		panic(err)
	}
	// Mock DPLL pins
	for _, pin := range *pins {
		DpllPins = append(DpllPins, &pin)
	}
}

func loadPins(path string) (*[]dpll_netlink.PinInfo, error) {
	pins := &[]dpll_netlink.PinInfo{}
	ptext, err := os.ReadFile(path)
	if err != nil {
		return pins, err
	}
	err = json.Unmarshal(ptext, pins)
	return pins, err
}

// FileSystemInterface defines the interface for filesystem operations to enable mocking
type FileSystemInterface interface {
	ReadDir(dirname string) ([]os.DirEntry, error)
	WriteFile(filename string, data []byte, perm os.FileMode) error
	ReadFile(filename string) ([]byte, error)
}

// RealFileSystem implements FileSystemInterface using real OS operations
type RealFileSystem struct{}

// ReadDir reads the contents of the directory specified by dirname
func (fs *RealFileSystem) ReadDir(dirname string) ([]os.DirEntry, error) {
	return os.ReadDir(dirname)
}

// WriteFile writes the data to the file specified by filename
func (fs *RealFileSystem) WriteFile(filename string, data []byte, perm os.FileMode) error {
	return os.WriteFile(filename, data, perm)
}

// ReadFile reads the data from the file specified by the filename
func (fs *RealFileSystem) ReadFile(filename string) ([]byte, error) {
	return os.ReadFile(filename)
}

// Default filesystem implementation
var filesystem FileSystemInterface = &RealFileSystem{}
