package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"wechat-bot/internal/workspacefs"
)

// Preserve the existing private catalogue, including fields from older importers.
func (m *Manager) editEnrollmentCatalog(remove map[string]bool, add *voiceEntry) error {
	dir := m.privateVoiceLibrary()
	r, err := workspacefs.EnsurePath(dir)
	if err != nil {
		return err
	}
	defer r.Close()
	obj := map[string]json.RawMessage{"version": json.RawMessage("1")}
	f, err := workspacefs.OpenRegular(r, "catalog.json")
	if err == nil {
		defer f.Close()
		st, e := f.Stat()
		if e != nil || st.Size() > 8<<20 {
			return errors.New("private_voice_catalog_invalid_size")
		}
		if json.NewDecoder(f).Decode(&obj) != nil || obj == nil {
			return errors.New("private_voice_catalog_invalid")
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return err
	}
	var items []json.RawMessage
	if raw, ok := obj["items"]; ok {
		if json.Unmarshal(raw, &items) != nil {
			return errors.New("invalid_voice_catalog_items")
		}
	}
	keep := make([]json.RawMessage, 0, len(items)+1)
	for _, item := range items {
		var v struct{ ID string }
		if err = json.Unmarshal(item, &v); err != nil {
			return err
		}
		if !remove[v.ID] {
			keep = append(keep, item)
		}
	}
	if add != nil {
		b, e := json.Marshal(add)
		if e != nil {
			return e
		}
		keep = append(keep, b)
	}
	obj["items"], err = json.Marshal(keep)
	if err != nil {
		return err
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, "catalog.json"), b)
}

func copyEnrollmentOutput(srcDir, srcName, destDir, destName, hash string) error {
	src, err := workspacefs.EnsurePath(srcDir)
	if err != nil {
		return err
	}
	defer src.Close()
	in, err := workspacefs.OpenRegular(src, srcName)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if st.Size() <= 0 {
		return io.ErrUnexpectedEOF
	}
	out, err := workspacefs.EnsurePath(destDir)
	if err != nil {
		return err
	}
	defer out.Close()
	return publishVerifiedStream(out, destName, in, st.Size(), hash)
}

func (m *Manager) registerEnrollmentVoice(pr enrollmentProfile, j enrollmentJob, result enrollmentResult) (string, error) {
	id := "speech-" + agentID(pr.ID, j.ID)[:24]
	entry := voiceEntry{ID: id, Name: pr.Name, Role: pr.Name, Owner: pr.Owner, Engine: "GPT-SoVITS", Status: "trained", Recommended: map[string]string{}}
	rel := filepath.Join("enrolled", pr.ID, id)
	dir := filepath.Join(m.privateVoiceLibrary(), rel)
	for _, f := range []struct{ name, kind, hash string }{{"gpt.ckpt", "gpt", result.GPTSHA}, {"sovits.pth", "sovits", result.SoVITSSHA}, {"reference.wav", "reference", result.ReferenceSHA}, {"preview.wav", "preview", result.PreviewSHA}} {
		if err := copyEnrollmentOutput(result.Dir, f.name, dir, f.name, f.hash); err != nil {
			return "", err
		}
		st, err := os.Stat(filepath.Join(dir, f.name))
		if err != nil {
			return "", err
		}
		v := voiceFile{Relative: filepath.ToSlash(filepath.Join(rel, f.name)), Filename: f.name, Hash: f.hash, Bytes: st.Size(), Kind: f.kind}
		switch f.kind {
		case "reference":
			entry.References = append(entry.References, v)
		case "preview":
			entry.Previews = append(entry.Previews, v)
		default:
			entry.Weights = append(entry.Weights, v)
			entry.Recommended[f.kind] = voiceFileID(v)
		}
	}
	root, err := workspacefs.EnsurePath(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	t := []byte(result.ReferenceText)
	if len(t) == 0 {
		return "", errors.New("private_voice_reference_transcript_missing")
	}
	h := namedSpeechSHA(t)
	if err = publishVerifiedStream(root, "reference.txt", bytes.NewReader(t), int64(len(t)), h); err != nil {
		return "", err
	}
	entry.References = append(entry.References, voiceFile{Relative: filepath.ToSlash(filepath.Join(rel, "reference.txt")), Filename: "reference.txt", Hash: h, Bytes: int64(len(t))})
	return id, m.editEnrollmentCatalog(map[string]bool{id: true}, &entry)
}

func (m *Manager) removeEnrollmentVoices(pr enrollmentProfile) error {
	ids := map[string]bool{}
	for _, j := range pr.Jobs {
		if j.Action == "train" {
			ids["speech-"+agentID(pr.ID, j.ID)[:24]] = true
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if err := m.editEnrollmentCatalog(ids, nil); err != nil {
		return err
	}
	r, err := workspacefs.EnsurePath(filepath.Join(m.privateVoiceLibrary(), "enrolled"))
	if err != nil {
		return err
	}
	defer r.Close()
	return r.RemoveAll(pr.ID)
}
