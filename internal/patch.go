package internal

import (
	"bytes"
	"fmt"
	"github.com/bluekeyes/go-gitdiff/gitdiff"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/mstg/srpmproc/internal/directives"
	srpmprocpb "github.com/mstg/srpmproc/pb"
	"google.golang.org/protobuf/encoding/prototext"
	"io/ioutil"
	"log"
	"path/filepath"
	"strings"
)

func srpmPatches(patchTree *git.Worktree, pushTree *git.Worktree) {
	// check SRPM patches
	_, err := patchTree.Filesystem.Stat("ROCKY/SRPM")
	if err == nil {
		// iterate through patches
		infos, err := patchTree.Filesystem.ReadDir("ROCKY/SRPM")
		if err != nil {
			log.Fatalf("could not walk patches: %v", err)
		}

		for _, info := range infos {
			// can only process .patch files
			if !strings.HasSuffix(info.Name(), ".patch") {
				continue
			}

			log.Printf("applying %s", info.Name())
			filePath := filepath.Join("ROCKY/SRPM", info.Name())

			patch, err := patchTree.Filesystem.Open(filePath)
			if err != nil {
				log.Fatalf("could not open patch file %s: %v", info.Name(), err)
			}
			files, _, err := gitdiff.Parse(patch)
			if err != nil {
				log.Fatalf("could not parse patch file: %v", err)
			}

			for _, patchedFile := range files {
				srcPath := patchedFile.NewName
				if !strings.HasPrefix(srcPath, "SPECS") {
					srcPath = filepath.Join("SOURCES", patchedFile.NewName)
				}
				var output bytes.Buffer
				if !patchedFile.IsDelete && !patchedFile.IsNew {
					patchSubjectFile, err := pushTree.Filesystem.Open(srcPath)
					if err != nil {
						log.Fatalf("could not open patch subject: %v", err)
					}

					err = gitdiff.NewApplier(patchSubjectFile).ApplyFile(&output, patchedFile)
					if err != nil {
						log.Fatalf("could not apply patch: %v", err)
					}
				}

				oldName := filepath.Join("SOURCES", patchedFile.OldName)
				_ = pushTree.Filesystem.Remove(oldName)
				_ = pushTree.Filesystem.Remove(srcPath)

				if patchedFile.IsNew {
					newFile, err := pushTree.Filesystem.Create(srcPath)
					if err != nil {
						log.Fatalf("could not create new file: %v", err)
					}
					err = gitdiff.NewApplier(newFile).ApplyFile(&output, patchedFile)
					if err != nil {
						log.Fatalf("could not apply patch: %v", err)
					}
					_, err = newFile.Write(output.Bytes())
					if err != nil {
						log.Fatalf("could not write post-patch file: %v", err)
					}
					_, err = pushTree.Add(srcPath)
					if err != nil {
						log.Fatalf("could not add file %s to git: %v", srcPath, err)
					}
					log.Printf("git add %s", srcPath)
				} else if !patchedFile.IsDelete {
					newFile, err := pushTree.Filesystem.Create(srcPath)
					if err != nil {
						log.Fatalf("could not create post-patch file: %v", err)
					}
					_, err = newFile.Write(output.Bytes())
					if err != nil {
						log.Fatalf("could not write post-patch file: %v", err)
					}
					_, err = pushTree.Add(srcPath)
					if err != nil {
						log.Fatalf("could not add file %s to git: %v", srcPath, err)
					}
					log.Printf("git add %s", srcPath)
				} else {
					_, err = pushTree.Remove(oldName)
					if err != nil {
						log.Fatalf("could not remove file %s to git: %v", oldName, err)
					}
					log.Printf("git rm %s", oldName)
				}
			}

			_, err = pushTree.Add(filePath)
			if err != nil {
				log.Fatalf("could not add file %s to git: %v", filePath, err)
			}
			log.Printf("git add %s", filePath)
		}
	}
}

func cfgPatches(patchTree *git.Worktree, pushTree *git.Worktree) {
	// check CFG patches
	_, err := patchTree.Filesystem.Stat("ROCKY/CFG")
	if err == nil {
		// iterate through patches
		infos, err := patchTree.Filesystem.ReadDir("ROCKY/CFG")
		if err != nil {
			log.Fatalf("could not walk patches: %v", err)
		}

		for _, info := range infos {
			// can only process .cfg files
			if !strings.HasSuffix(info.Name(), ".cfg") {
				continue
			}

			log.Printf("applying directive %s", info.Name())
			filePath := filepath.Join("ROCKY/CFG", info.Name())
			directive, err := patchTree.Filesystem.Open(filePath)
			if err != nil {
				log.Fatalf("could not open directive file %s: %v", info.Name(), err)
			}
			directiveBytes, err := ioutil.ReadAll(directive)
			if err != nil {
				log.Fatalf("could not read directive file: %v", err)
			}

			var cfg srpmprocpb.Cfg
			err = prototext.Unmarshal(directiveBytes, &cfg)
			if err != nil {
				log.Fatalf("could not unmarshal cfg file: %v", err)
			}

			directives.Apply(&cfg, patchTree, pushTree)
		}
	}
}

func applyPatches(patchTree *git.Worktree, pushTree *git.Worktree) {
	// check if patches exist
	_, err := patchTree.Filesystem.Stat("ROCKY")
	if err == nil {
		srpmPatches(patchTree, pushTree)
		cfgPatches(patchTree, pushTree)
	}
}

func executePatches(pd *ProcessData, md *modeData) {
	// fetch patch repository
	repo, err := git.Init(memory.NewStorage(), memfs.New())
	if err != nil {
		log.Fatalf("could not create new dist repo: %v", err)
	}
	w, err := repo.Worktree()
	if err != nil {
		log.Fatalf("could not get dist worktree: %v", err)
	}

	remoteUrl := fmt.Sprintf("%s/patch/%s.git", pd.UpstreamPrefix, md.rpmFile.Name())
	refspec := config.RefSpec(fmt.Sprintf("+refs/heads/*:refs/remotes/origin/*"))

	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name:  "origin",
		URLs:  []string{remoteUrl},
		Fetch: []config.RefSpec{refspec},
	})
	if err != nil {
		log.Fatalf("could not create remote: %v", err)
	}

	err = repo.Fetch(&git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{refspec},
		Auth:       pd.Authenticator,
	})

	refName := plumbing.NewBranchReferenceName(md.pushBranch)
	log.Printf("set reference to ref: %s", refName)

	if err != nil {
		// no patches active
		log.Println("info: patch repo not found")
		return
	} else {
		err = w.Checkout(&git.CheckoutOptions{
			Branch: plumbing.NewRemoteReferenceName("origin", "master"),
			Force:  true,
		})
		// common patches found, apply them
		if err == nil {
			applyPatches(w, md.worktree)
		} else {
			log.Println("info: no common patches found")
		}

		err = w.Checkout(&git.CheckoutOptions{
			Branch: plumbing.NewRemoteReferenceName("origin", md.pushBranch),
			Force:  true,
		})
		// branch specific patches found, apply them
		if err == nil {
			applyPatches(w, md.worktree)
		} else {
			log.Println("info: no branch specific patches found")
		}
	}
}
