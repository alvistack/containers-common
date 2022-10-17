package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/containers/storage/pkg/ioutils"
	"github.com/containers/storage/pkg/stringid"
	"github.com/containers/storage/pkg/stringutils"
	"github.com/containers/storage/pkg/truncindex"
	digest "github.com/opencontainers/go-digest"
)

const (
	// ImageDigestManifestBigDataNamePrefix is a prefix of big data item
	// names which we consider to be manifests, used for computing a
	// "digest" value for the image as a whole, by which we can locate the
	// image later.
	ImageDigestManifestBigDataNamePrefix = "manifest"
	// ImageDigestBigDataKey is provided for compatibility with older
	// versions of the image library.  It will be removed in the future.
	ImageDigestBigDataKey = "manifest"
)

// An Image is a reference to a layer and an associated metadata string.
type Image struct {
	// ID is either one which was specified at create-time, or a random
	// value which was generated by the library.
	ID string `json:"id"`

	// Digest is a digest value that we can use to locate the image, if one
	// was specified at creation-time.
	Digest digest.Digest `json:"digest,omitempty"`

	// Digests is a list of digest values of the image's manifests, and
	// possibly a manually-specified value, that we can use to locate the
	// image.  If Digest is set, its value is also in this list.
	Digests []digest.Digest `json:"-"`

	// Names is an optional set of user-defined convenience values.  The
	// image can be referred to by its ID or any of its names.  Names are
	// unique among images, and are often the text representation of tagged
	// or canonical references.
	Names []string `json:"names,omitempty"`

	// NamesHistory is an optional set of Names the image had in the past. The
	// contained names are free from any duplicates, whereas the newest entry
	// is the first one.
	NamesHistory []string `json:"names-history,omitempty"`

	// TopLayer is the ID of the topmost layer of the image itself, if the
	// image contains one or more layers.  Multiple images can refer to the
	// same top layer.
	TopLayer string `json:"layer,omitempty"`

	// MappedTopLayers are the IDs of alternate versions of the top layer
	// which have the same contents and parent, and which differ from
	// TopLayer only in which ID mappings they use.  When the image is
	// to be removed, they should be removed before the TopLayer, as the
	// graph driver may depend on that.
	MappedTopLayers []string `json:"mapped-layers,omitempty"`

	// Metadata is data we keep for the convenience of the caller.  It is not
	// expected to be large, since it is kept in memory.
	Metadata string `json:"metadata,omitempty"`

	// BigDataNames is a list of names of data items that we keep for the
	// convenience of the caller.  They can be large, and are only in
	// memory when being read from or written to disk.
	BigDataNames []string `json:"big-data-names,omitempty"`

	// BigDataSizes maps the names in BigDataNames to the sizes of the data
	// that has been stored, if they're known.
	BigDataSizes map[string]int64 `json:"big-data-sizes,omitempty"`

	// BigDataDigests maps the names in BigDataNames to the digests of the
	// data that has been stored, if they're known.
	BigDataDigests map[string]digest.Digest `json:"big-data-digests,omitempty"`

	// Created is the datestamp for when this image was created.  Older
	// versions of the library did not track this information, so callers
	// will likely want to use the IsZero() method to verify that a value
	// is set before using it.
	Created time.Time `json:"created,omitempty"`

	// ReadOnly is true if this image resides in a read-only layer store.
	ReadOnly bool `json:"-"`

	Flags map[string]interface{} `json:"flags,omitempty"`
}

// roImageStore provides bookkeeping for information about Images.
type roImageStore interface {
	roFileBasedStore
	roMetadataStore
	roBigDataStore

	// Exists checks if there is an image with the given ID or name.
	Exists(id string) bool

	// Get retrieves information about an image given an ID or name.
	Get(id string) (*Image, error)

	// Images returns a slice enumerating the known images.
	Images() ([]Image, error)

	// ByDigest returns a slice enumerating the images which have either an
	// explicitly-set digest, or a big data item with a name that starts
	// with ImageDigestManifestBigDataNamePrefix, which matches the
	// specified digest.
	ByDigest(d digest.Digest) ([]*Image, error)
}

// rwImageStore provides bookkeeping for information about Images.
type rwImageStore interface {
	roImageStore
	rwFileBasedStore
	rwMetadataStore
	rwImageBigDataStore
	flaggableStore

	// Create creates an image that has a specified ID (or a random one) and
	// optional names, using the specified layer as its topmost (hopefully
	// read-only) layer.  That layer can be referenced by multiple images.
	Create(id string, names []string, layer, metadata string, created time.Time, searchableDigest digest.Digest) (*Image, error)

	// updateNames modifies names associated with an image based on (op, names).
	// The values are expected to be valid normalized
	// named image references.
	updateNames(id string, names []string, op updateNameOperation) error

	// Delete removes the record of the image.
	Delete(id string) error

	// Wipe removes records of all images.
	Wipe() error
}

type imageStore struct {
	lockfile Locker
	dir      string
	images   []*Image
	idindex  *truncindex.TruncIndex
	byid     map[string]*Image
	byname   map[string]*Image
	bydigest map[digest.Digest][]*Image
	loadMut  sync.Mutex
}

func copyImage(i *Image) *Image {
	return &Image{
		ID:              i.ID,
		Digest:          i.Digest,
		Digests:         copyDigestSlice(i.Digests),
		Names:           copyStringSlice(i.Names),
		NamesHistory:    copyStringSlice(i.NamesHistory),
		TopLayer:        i.TopLayer,
		MappedTopLayers: copyStringSlice(i.MappedTopLayers),
		Metadata:        i.Metadata,
		BigDataNames:    copyStringSlice(i.BigDataNames),
		BigDataSizes:    copyStringInt64Map(i.BigDataSizes),
		BigDataDigests:  copyStringDigestMap(i.BigDataDigests),
		Created:         i.Created,
		ReadOnly:        i.ReadOnly,
		Flags:           copyStringInterfaceMap(i.Flags),
	}
}

func copyImageSlice(slice []*Image) []*Image {
	if len(slice) > 0 {
		cp := make([]*Image, len(slice))
		for i := range slice {
			cp[i] = copyImage(slice[i])
		}
		return cp
	}
	return nil
}

func (r *imageStore) Images() ([]Image, error) {
	images := make([]Image, len(r.images))
	for i := range r.images {
		images[i] = *copyImage(r.images[i])
	}
	return images, nil
}

func (r *imageStore) imagespath() string {
	return filepath.Join(r.dir, "images.json")
}

func (r *imageStore) datadir(id string) string {
	return filepath.Join(r.dir, id)
}

func (r *imageStore) datapath(id, key string) string {
	return filepath.Join(r.datadir(id), makeBigDataBaseName(key))
}

// bigDataNameIsManifest determines if a big data item with the specified name
// is considered to be representative of the image, in that its digest can be
// said to also be the image's digest.  Currently, if its name is, or begins
// with, "manifest", we say that it is.
func bigDataNameIsManifest(name string) bool {
	return strings.HasPrefix(name, ImageDigestManifestBigDataNamePrefix)
}

// recomputeDigests takes a fixed digest and a name-to-digest map and builds a
// list of the unique values that would identify the image.
func (i *Image) recomputeDigests() error {
	validDigests := make([]digest.Digest, 0, len(i.BigDataDigests)+1)
	digests := make(map[digest.Digest]struct{})
	if i.Digest != "" {
		if err := i.Digest.Validate(); err != nil {
			return fmt.Errorf("validating image digest %q: %w", string(i.Digest), err)
		}
		digests[i.Digest] = struct{}{}
		validDigests = append(validDigests, i.Digest)
	}
	for name, digest := range i.BigDataDigests {
		if !bigDataNameIsManifest(name) {
			continue
		}
		if err := digest.Validate(); err != nil {
			return fmt.Errorf("validating digest %q for big data item %q: %w", string(digest), name, err)
		}
		// Deduplicate the digest values.
		if _, known := digests[digest]; !known {
			digests[digest] = struct{}{}
			validDigests = append(validDigests, digest)
		}
	}
	if i.Digest == "" && len(validDigests) > 0 {
		i.Digest = validDigests[0]
	}
	i.Digests = validDigests
	return nil
}

func (r *imageStore) Load() error {
	shouldSave := false
	rpath := r.imagespath()
	data, err := os.ReadFile(rpath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	images := []*Image{}
	idlist := []string{}
	ids := make(map[string]*Image)
	names := make(map[string]*Image)
	digests := make(map[digest.Digest][]*Image)
	if err = json.Unmarshal(data, &images); len(data) == 0 || err == nil {
		idlist = make([]string, 0, len(images))
		for n, image := range images {
			ids[image.ID] = images[n]
			idlist = append(idlist, image.ID)
			for _, name := range image.Names {
				if conflict, ok := names[name]; ok {
					r.removeName(conflict, name)
					shouldSave = true
				}
			}
			// Compute the digest list.
			err = image.recomputeDigests()
			if err != nil {
				return fmt.Errorf("computing digests for image with ID %q (%v): %w", image.ID, image.Names, err)
			}
			for _, name := range image.Names {
				names[name] = image
			}
			for _, digest := range image.Digests {
				list := digests[digest]
				digests[digest] = append(list, image)
			}
			image.ReadOnly = !r.IsReadWrite()
		}
	}
	if shouldSave && (!r.IsReadWrite() || !r.Locked()) {
		return ErrDuplicateImageNames
	}
	r.images = images
	r.idindex = truncindex.NewTruncIndex(idlist) // Invalid values in idlist are ignored: they are not a reason to refuse processing the whole store.
	r.byid = ids
	r.byname = names
	r.bydigest = digests
	if shouldSave {
		return r.Save()
	}
	return nil
}

func (r *imageStore) Save() error {
	if !r.IsReadWrite() {
		return fmt.Errorf("not allowed to modify the image store at %q: %w", r.imagespath(), ErrStoreIsReadOnly)
	}
	if !r.Locked() {
		return errors.New("image store is not locked for writing")
	}
	rpath := r.imagespath()
	if err := os.MkdirAll(filepath.Dir(rpath), 0700); err != nil {
		return err
	}
	jdata, err := json.Marshal(&r.images)
	if err != nil {
		return err
	}
	if err := ioutils.AtomicWriteFile(rpath, jdata, 0600); err != nil {
		return err
	}
	return r.Touch()
}

func newImageStore(dir string) (rwImageStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lockfile, err := GetLockfile(filepath.Join(dir, "images.lock"))
	if err != nil {
		return nil, err
	}
	istore := imageStore{
		lockfile: lockfile,
		dir:      dir,
		images:   []*Image{},
		byid:     make(map[string]*Image),
		byname:   make(map[string]*Image),
		bydigest: make(map[digest.Digest][]*Image),
	}
	istore.Lock()
	defer istore.Unlock()
	if err := istore.Load(); err != nil {
		return nil, err
	}
	return &istore, nil
}

func newROImageStore(dir string) (roImageStore, error) {
	lockfile, err := GetROLockfile(filepath.Join(dir, "images.lock"))
	if err != nil {
		return nil, err
	}
	istore := imageStore{
		lockfile: lockfile,
		dir:      dir,
		images:   []*Image{},
		byid:     make(map[string]*Image),
		byname:   make(map[string]*Image),
		bydigest: make(map[digest.Digest][]*Image),
	}
	istore.RLock()
	defer istore.Unlock()
	if err := istore.Load(); err != nil {
		return nil, err
	}
	return &istore, nil
}

func (r *imageStore) lookup(id string) (*Image, bool) {
	if image, ok := r.byid[id]; ok {
		return image, ok
	} else if image, ok := r.byname[id]; ok {
		return image, ok
	} else if longid, err := r.idindex.Get(id); err == nil {
		image, ok := r.byid[longid]
		return image, ok
	}
	return nil, false
}

func (r *imageStore) ClearFlag(id string, flag string) error {
	if !r.IsReadWrite() {
		return fmt.Errorf("not allowed to clear flags on images at %q: %w", r.imagespath(), ErrStoreIsReadOnly)
	}
	image, ok := r.lookup(id)
	if !ok {
		return fmt.Errorf("locating image with ID %q: %w", id, ErrImageUnknown)
	}
	delete(image.Flags, flag)
	return r.Save()
}

func (r *imageStore) SetFlag(id string, flag string, value interface{}) error {
	if !r.IsReadWrite() {
		return fmt.Errorf("not allowed to set flags on images at %q: %w", r.imagespath(), ErrStoreIsReadOnly)
	}
	image, ok := r.lookup(id)
	if !ok {
		return fmt.Errorf("locating image with ID %q: %w", id, ErrImageUnknown)
	}
	if image.Flags == nil {
		image.Flags = make(map[string]interface{})
	}
	image.Flags[flag] = value
	return r.Save()
}

func (r *imageStore) Create(id string, names []string, layer, metadata string, created time.Time, searchableDigest digest.Digest) (image *Image, err error) {
	if !r.IsReadWrite() {
		return nil, fmt.Errorf("not allowed to create new images at %q: %w", r.imagespath(), ErrStoreIsReadOnly)
	}
	if id == "" {
		id = stringid.GenerateRandomID()
		_, idInUse := r.byid[id]
		for idInUse {
			id = stringid.GenerateRandomID()
			_, idInUse = r.byid[id]
		}
	}
	if _, idInUse := r.byid[id]; idInUse {
		return nil, fmt.Errorf("an image with ID %q already exists: %w", id, ErrDuplicateID)
	}
	names = dedupeNames(names)
	for _, name := range names {
		if image, nameInUse := r.byname[name]; nameInUse {
			return nil, fmt.Errorf("image name %q is already associated with image %q: %w", name, image.ID, ErrDuplicateName)
		}
	}
	if created.IsZero() {
		created = time.Now().UTC()
	}

	image = &Image{
		ID:             id,
		Digest:         searchableDigest,
		Digests:        nil,
		Names:          names,
		TopLayer:       layer,
		Metadata:       metadata,
		BigDataNames:   []string{},
		BigDataSizes:   make(map[string]int64),
		BigDataDigests: make(map[string]digest.Digest),
		Created:        created,
		Flags:          make(map[string]interface{}),
	}
	err = image.recomputeDigests()
	if err != nil {
		return nil, fmt.Errorf("validating digests for new image: %w", err)
	}
	r.images = append(r.images, image)
	// This can only fail on duplicate IDs, which shouldn’t happen — and in that case the index is already in the desired state anyway.
	// Implementing recovery from an unlikely and unimportant failure here would be too risky.
	_ = r.idindex.Add(id)
	r.byid[id] = image
	for _, name := range names {
		r.byname[name] = image
	}
	for _, digest := range image.Digests {
		list := r.bydigest[digest]
		r.bydigest[digest] = append(list, image)
	}
	err = r.Save()
	image = copyImage(image)
	return image, err
}

func (r *imageStore) addMappedTopLayer(id, layer string) error {
	if image, ok := r.lookup(id); ok {
		image.MappedTopLayers = append(image.MappedTopLayers, layer)
		return r.Save()
	}
	return fmt.Errorf("locating image with ID %q: %w", id, ErrImageUnknown)
}

func (r *imageStore) removeMappedTopLayer(id, layer string) error {
	if image, ok := r.lookup(id); ok {
		initialLen := len(image.MappedTopLayers)
		image.MappedTopLayers = stringutils.RemoveFromSlice(image.MappedTopLayers, layer)
		// No layer was removed.  No need to save.
		if initialLen == len(image.MappedTopLayers) {
			return nil
		}
		return r.Save()
	}
	return fmt.Errorf("locating image with ID %q: %w", id, ErrImageUnknown)
}

func (r *imageStore) Metadata(id string) (string, error) {
	if image, ok := r.lookup(id); ok {
		return image.Metadata, nil
	}
	return "", fmt.Errorf("locating image with ID %q: %w", id, ErrImageUnknown)
}

func (r *imageStore) SetMetadata(id, metadata string) error {
	if !r.IsReadWrite() {
		return fmt.Errorf("not allowed to modify image metadata at %q: %w", r.imagespath(), ErrStoreIsReadOnly)
	}
	if image, ok := r.lookup(id); ok {
		image.Metadata = metadata
		return r.Save()
	}
	return fmt.Errorf("locating image with ID %q: %w", id, ErrImageUnknown)
}

func (r *imageStore) removeName(image *Image, name string) {
	image.Names = stringSliceWithoutValue(image.Names, name)
}

func (i *Image) addNameToHistory(name string) {
	i.NamesHistory = dedupeNames(append([]string{name}, i.NamesHistory...))
}

func (r *imageStore) updateNames(id string, names []string, op updateNameOperation) error {
	if !r.IsReadWrite() {
		return fmt.Errorf("not allowed to change image name assignments at %q: %w", r.imagespath(), ErrStoreIsReadOnly)
	}
	image, ok := r.lookup(id)
	if !ok {
		return fmt.Errorf("locating image with ID %q: %w", id, ErrImageUnknown)
	}
	oldNames := image.Names
	names, err := applyNameOperation(oldNames, names, op)
	if err != nil {
		return err
	}
	for _, name := range oldNames {
		delete(r.byname, name)
	}
	for _, name := range names {
		if otherImage, ok := r.byname[name]; ok {
			r.removeName(otherImage, name)
		}
		r.byname[name] = image
		image.addNameToHistory(name)
	}
	image.Names = names
	return r.Save()
}

func (r *imageStore) Delete(id string) error {
	if !r.IsReadWrite() {
		return fmt.Errorf("not allowed to delete images at %q: %w", r.imagespath(), ErrStoreIsReadOnly)
	}
	image, ok := r.lookup(id)
	if !ok {
		return fmt.Errorf("locating image with ID %q: %w", id, ErrImageUnknown)
	}
	id = image.ID
	toDeleteIndex := -1
	for i, candidate := range r.images {
		if candidate.ID == id {
			toDeleteIndex = i
		}
	}
	delete(r.byid, id)
	// This can only fail if the ID is already missing, which shouldn’t happen — and in that case the index is already in the desired state anyway.
	// The store’s Delete method is used on various paths to recover from failures, so this should be robust against partially missing data.
	_ = r.idindex.Delete(id)
	for _, name := range image.Names {
		delete(r.byname, name)
	}
	for _, digest := range image.Digests {
		prunedList := imageSliceWithoutValue(r.bydigest[digest], image)
		if len(prunedList) == 0 {
			delete(r.bydigest, digest)
		} else {
			r.bydigest[digest] = prunedList
		}
	}
	if toDeleteIndex != -1 {
		// delete the image at toDeleteIndex
		if toDeleteIndex == len(r.images)-1 {
			r.images = r.images[:len(r.images)-1]
		} else {
			r.images = append(r.images[:toDeleteIndex], r.images[toDeleteIndex+1:]...)
		}
	}
	if err := r.Save(); err != nil {
		return err
	}
	if err := os.RemoveAll(r.datadir(id)); err != nil {
		return err
	}
	return nil
}

func (r *imageStore) Get(id string) (*Image, error) {
	if image, ok := r.lookup(id); ok {
		return copyImage(image), nil
	}
	return nil, fmt.Errorf("locating image with ID %q: %w", id, ErrImageUnknown)
}

func (r *imageStore) Exists(id string) bool {
	_, ok := r.lookup(id)
	return ok
}

func (r *imageStore) ByDigest(d digest.Digest) ([]*Image, error) {
	if images, ok := r.bydigest[d]; ok {
		return copyImageSlice(images), nil
	}
	return nil, fmt.Errorf("locating image with digest %q: %w", d, ErrImageUnknown)
}

func (r *imageStore) BigData(id, key string) ([]byte, error) {
	if key == "" {
		return nil, fmt.Errorf("can't retrieve image big data value for empty name: %w", ErrInvalidBigDataName)
	}
	image, ok := r.lookup(id)
	if !ok {
		return nil, fmt.Errorf("locating image with ID %q: %w", id, ErrImageUnknown)
	}
	return os.ReadFile(r.datapath(image.ID, key))
}

func (r *imageStore) BigDataSize(id, key string) (int64, error) {
	if key == "" {
		return -1, fmt.Errorf("can't retrieve size of image big data with empty name: %w", ErrInvalidBigDataName)
	}
	image, ok := r.lookup(id)
	if !ok {
		return -1, fmt.Errorf("locating image with ID %q: %w", id, ErrImageUnknown)
	}
	if image.BigDataSizes == nil {
		image.BigDataSizes = make(map[string]int64)
	}
	if size, ok := image.BigDataSizes[key]; ok {
		return size, nil
	}
	if data, err := r.BigData(id, key); err == nil && data != nil {
		return int64(len(data)), nil
	}
	return -1, ErrSizeUnknown
}

func (r *imageStore) BigDataDigest(id, key string) (digest.Digest, error) {
	if key == "" {
		return "", fmt.Errorf("can't retrieve digest of image big data value with empty name: %w", ErrInvalidBigDataName)
	}
	image, ok := r.lookup(id)
	if !ok {
		return "", fmt.Errorf("locating image with ID %q: %w", id, ErrImageUnknown)
	}
	if image.BigDataDigests == nil {
		image.BigDataDigests = make(map[string]digest.Digest)
	}
	if d, ok := image.BigDataDigests[key]; ok {
		return d, nil
	}
	return "", ErrDigestUnknown
}

func (r *imageStore) BigDataNames(id string) ([]string, error) {
	image, ok := r.lookup(id)
	if !ok {
		return nil, fmt.Errorf("locating image with ID %q: %w", id, ErrImageUnknown)
	}
	return copyStringSlice(image.BigDataNames), nil
}

func imageSliceWithoutValue(slice []*Image, value *Image) []*Image {
	modified := make([]*Image, 0, len(slice))
	for _, v := range slice {
		if v == value {
			continue
		}
		modified = append(modified, v)
	}
	return modified
}

func (r *imageStore) SetBigData(id, key string, data []byte, digestManifest func([]byte) (digest.Digest, error)) error {
	if key == "" {
		return fmt.Errorf("can't set empty name for image big data item: %w", ErrInvalidBigDataName)
	}
	if !r.IsReadWrite() {
		return fmt.Errorf("not allowed to save data items associated with images at %q: %w", r.imagespath(), ErrStoreIsReadOnly)
	}
	image, ok := r.lookup(id)
	if !ok {
		return fmt.Errorf("locating image with ID %q: %w", id, ErrImageUnknown)
	}
	err := os.MkdirAll(r.datadir(image.ID), 0700)
	if err != nil {
		return err
	}
	var newDigest digest.Digest
	if bigDataNameIsManifest(key) {
		if digestManifest == nil {
			return fmt.Errorf("digesting manifest: no manifest digest callback provided: %w", ErrDigestUnknown)
		}
		if newDigest, err = digestManifest(data); err != nil {
			return fmt.Errorf("digesting manifest: %w", err)
		}
	} else {
		newDigest = digest.Canonical.FromBytes(data)
	}
	err = ioutils.AtomicWriteFile(r.datapath(image.ID, key), data, 0600)
	if err == nil {
		save := false
		if image.BigDataSizes == nil {
			image.BigDataSizes = make(map[string]int64)
		}
		oldSize, sizeOk := image.BigDataSizes[key]
		image.BigDataSizes[key] = int64(len(data))
		if image.BigDataDigests == nil {
			image.BigDataDigests = make(map[string]digest.Digest)
		}
		oldDigest, digestOk := image.BigDataDigests[key]
		image.BigDataDigests[key] = newDigest
		if !sizeOk || oldSize != image.BigDataSizes[key] || !digestOk || oldDigest != newDigest {
			save = true
		}
		addName := true
		for _, name := range image.BigDataNames {
			if name == key {
				addName = false
				break
			}
		}
		if addName {
			image.BigDataNames = append(image.BigDataNames, key)
			save = true
		}
		for _, oldDigest := range image.Digests {
			// remove the image from the list of images in the digest-based index
			if list, ok := r.bydigest[oldDigest]; ok {
				prunedList := imageSliceWithoutValue(list, image)
				if len(prunedList) == 0 {
					delete(r.bydigest, oldDigest)
				} else {
					r.bydigest[oldDigest] = prunedList
				}
			}
		}
		if err = image.recomputeDigests(); err != nil {
			return fmt.Errorf("loading recomputing image digest information for %s: %w", image.ID, err)
		}
		for _, newDigest := range image.Digests {
			// add the image to the list of images in the digest-based index which
			// corresponds to the new digest for this item, unless it's already there
			list := r.bydigest[newDigest]
			if len(list) == len(imageSliceWithoutValue(list, image)) {
				// the list isn't shortened by trying to prune this image from it,
				// so it's not in there yet
				r.bydigest[newDigest] = append(list, image)
			}
		}
		if save {
			err = r.Save()
		}
	}
	return err
}

func (r *imageStore) Wipe() error {
	if !r.IsReadWrite() {
		return fmt.Errorf("not allowed to delete images at %q: %w", r.imagespath(), ErrStoreIsReadOnly)
	}
	ids := make([]string, 0, len(r.byid))
	for id := range r.byid {
		ids = append(ids, id)
	}
	for _, id := range ids {
		if err := r.Delete(id); err != nil {
			return err
		}
	}
	return nil
}

func (r *imageStore) Lock() {
	r.lockfile.Lock()
}

func (r *imageStore) RLock() {
	r.lockfile.RLock()
}

func (r *imageStore) Unlock() {
	r.lockfile.Unlock()
}

func (r *imageStore) Touch() error {
	return r.lockfile.Touch()
}

func (r *imageStore) Modified() (bool, error) {
	return r.lockfile.Modified()
}

func (r *imageStore) IsReadWrite() bool {
	return r.lockfile.IsReadWrite()
}

func (r *imageStore) TouchedSince(when time.Time) bool {
	return r.lockfile.TouchedSince(when)
}

func (r *imageStore) Locked() bool {
	return r.lockfile.Locked()
}

func (r *imageStore) ReloadIfChanged() error {
	r.loadMut.Lock()
	defer r.loadMut.Unlock()

	modified, err := r.Modified()
	if err == nil && modified {
		return r.Load()
	}
	return err
}
