package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
	"gopkg.in/src-d/go-git.v4/storage/memory"
)

// User account modelled in a json file
type User struct {
	Username string   `json:"username"`
	Comment  string   `json:"comment"`
	Password string   `json:"password"`
	Shell    string   `json:"shell"`
	Home     string   `json:"home"`
	Groups   []string `json:"groups"`
	Realms   []string `json:"realms"`
	SSHKeys  []string `json:"ssh_keys"`
}

var (
	debug  bool
	realm  string
	repo   string
	distro distroCommands
)

func init() {
	log.SetPrefix("userd v1.18 ")
	if os.Geteuid() != 0 {
		log.Fatalf("Error: Bad user id (%d), must run as root", os.Geteuid())
	}

	flag.StringVar(&realm, "realm", "", "the instance's realm eg: dev, stage, prod")
	flag.StringVar(&repo, "repo", "", "git repo where users are stored")
	flag.BoolVar(&debug, "debug", false, "print debugging info")
	flag.Parse()

	if realm == "" {
		log.Fatal("Error: Empty argument --realm")
	}
	if repo == "" {
		log.Fatal("Error: Empty argument --repo")
	}

	v := GetOS()
	if v == "" {
		log.Fatal("Unable to detect operating system")
	}
	distro = GetOSCommands(v)
}

// for debugging
func info(msg string) {
	if debug {
		log.Printf("DEBUG: %s", msg)
	}
}

// clone a git repo full of json users into memory
func gitClone(repo string) *git.Repository {
	r, err := git.Clone(memory.NewStorage(), nil, &git.CloneOptions{
		URL:   repo,
		Depth: 1,
	})
	if err != nil {
		log.Fatal("git clone ", repo, ": Error: ", err)
	}
	log.Print("git clone ", repo)
	return r
}

// gather all the users together who are meant to be in this instance's realm
func gatherRepoUsers(repo string, r *git.Repository, realm string) (users []User) {
	ref, _ := r.Head()
	commit, _ := r.CommitObject(ref.Hash())
	tree, _ := commit.Tree()

	tree.Files().ForEach(func(f *object.File) error {
		var u User
		if len(f.Name) > 5 && strings.ToLower(f.Name[len(f.Name)-5:]) == ".json" {
			content, _ := f.Contents()
			compact := strings.Join(strings.Fields(content), "")
			err := json.Unmarshal([]byte(content), &u)
			if err != nil {
				log.Printf("%s: Error: Parse or type error in JSON: %s", f.Name, compact)
			} else if u.Username == "" {
				log.Printf("%s: Error: Missing 'username' in JSON: %s", f.Name, compact)
			} else {
				if u.Home == "" {
					u.Home = "/home/" + u.Username
				} else {
					u.Home = path.Clean(u.Home)
				}
				if u.Shell == "" {
					u.Shell = "/bin/bash"
				}
				// sort them now, to make string comparisons simpler later on
				sort.Strings(u.SSHKeys)
				u.Groups = removeInvalidGroups(u.Groups, u.Username, realm)
				users = append(users, u)
			}
		}
		return nil
	})

	sort.Slice(users, func(i, j int) bool {
		return users[i].Username < users[j].Username
	})
	return users
}

// check the groups that are available on this system
func removeInvalidGroups(groups []string, username string, realm string) (goodGroups []string) {
	for _, g := range groups {
		// per realm groups, eg: sudo:realm1:realm2:realm3
		if gr := strings.Split(g, ":"); len(gr) > 1 {
			g = gr[0]
			if !inRangePattern(realm, gr[1:]) {
				continue
			}
		}
		// ignore user's primary group, shouldn't mess with that
		if g == username {
			continue
		}
		// only include groups that exist on this instance
		if _, err := user.LookupGroup(g); err == nil {
			goodGroups = append(goodGroups, g)
		}
	}
	sort.Strings(goodGroups)
	return goodGroups
}

// check if a user account exists on this system
func userExists(username string) bool {
	if _, err := user.Lookup(username); err == nil {
		return true
	}
	return false
}

// create a new user account
func createUser(attrs User) bool {
	log.Printf("Creating user: %s", attrs.Username)
	args := distro.addUser(attrs.Username, attrs.Home)
	if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		log.Printf("Error: Can't create user: %s: %s %s", attrs.Username, err, out)
		return false
	}
	return true
}

// delete a user account
func deleteUser(username string) bool {
	log.Printf("Deleting user: %s", username)
	args := distro.delUser(username)
	if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		log.Printf("Error: Can't delete user: %s: %s %s", username, err, out)
		return false
	}
	return true
}

// update the details of an existing user account
func updateUser(attrs User) bool {
	outp, _ := exec.Command("getent", "shadow", attrs.Username).CombinedOutput()

	var currentPassword string
	if strings.Contains(string(outp), ":") {
		currentPassword = strings.TrimSpace(strings.Split(string(outp), ":")[1])
	}
	outs, _ := exec.Command("getent", "passwd", attrs.Username).CombinedOutput()
	currentShell := strings.TrimSpace(strings.Split(string(outs), ":")[6])
	currentHome := strings.TrimSpace(strings.Split(string(outs), ":")[5])
	currentComment := strings.TrimSpace(strings.Split(string(outs), ":")[4])
	existingGroups := getUserGroups(attrs.Username)

	if attrs.Shell != currentShell {
		updateShell(attrs.Username, attrs.Shell)
	}
	if attrs.Password != currentPassword {
		updatePassword(attrs.Username, attrs.Password)
	}
	if attrs.Home != currentHome {
		updateHome(attrs.Username, attrs.Home)
	}
	if attrs.Comment != currentComment {
		updateComment(attrs.Username, attrs.Comment)
	}
	if strings.Join(existingGroups, ",") != strings.Join(attrs.Groups, ",") {
		updateGroups(attrs.Username, attrs.Groups)
	}

	keyFile := path.Join(attrs.Home, ".ssh", "authorized_keys")
	fileData := []string{}
	if buf, err := ioutil.ReadFile(keyFile); err == nil {
		fileData = strings.Split(string(buf), "\n")
		sort.Strings(fileData)
	}
	if strings.Join(attrs.SSHKeys, ",") != strings.Join(fileData, ",") {
		updateSSHPublicKeys(attrs.Username, attrs)
	}
	return true
}

// change user's default shell
func updateShell(username string, shell string) bool {
	log.Printf("Updating shell for %s to %s", username, shell)
	args := distro.changeShell(username, shell)
	if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		log.Printf("Error: Can't update shell for %s: %s %s", username, err, out)
		return false
	}
	return true
}

// change users password
func updatePassword(username string, password string) bool {
	log.Printf("Updating password for %s", username)
	info(fmt.Sprintf("New password: %s", password))
	args := distro.changePassword(username, password)
	if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		log.Printf("Error: Can't update password for %s: %s %s", username, err, out)
		return false
	}
	return true
}

// change users home directory
func updateHome(username string, home string) bool {
	log.Printf("Updating home dir for %s to %s", username, home)
	args := distro.changeHomeDir(username, home)
	if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		log.Printf("Error: Can't update home dir for %s: %s %s", username, err, out)
		return false
	}
	return true
}

// change users gecos comment
func updateComment(username string, comment string) bool {
	log.Printf("Updating comment for %s to %s", username, comment)
	args := distro.changeComment(username, comment)
	if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		log.Printf("Error: Can't update comment for %s: %s %s", username, err, out)
		return false
	}
	return true
}

// get the list of groups a user belongs to
func getUserGroups(username string) (groups []string) {
	u, _ := user.Lookup(username)
	gids, _ := u.GroupIds()
	for _, gid := range gids {
		group, _ := user.LookupGroupId(gid)
		if group.Name != username { // ignore the user's primary group (same name as username)
			groups = append(groups, group.Name)
		}
	}
	sort.Strings(groups)
	return groups
}

// change a users list of groups they belong to
func updateGroups(username string, groups []string) bool {
	if len(groups) > 0 {
		log.Printf("Updating user groups for %s: %s", username, groups)
		args := distro.changeGroups(username, strings.Join(groups, ","))
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			log.Printf("Error: Can't update user's groups for %s: %s %s", username, err, out)
			return false
		}
	}
	return true
}

// update the user's ~/.ssh/authorized_keys file with their public keys
func updateSSHPublicKeys(username string, attrs User) bool {
	keyFile := path.Join(attrs.Home, ".ssh", "authorized_keys")
	keyData := strings.Join(attrs.SSHKeys, "\n")
	tail := 0
	if len(keyData) > 50 {
		tail = len(keyData) - 50
	}
	log.Printf("Updating ssh keys for %s (...%s)", username, strings.TrimSpace(keyData[tail:]))
	info(keyData)
	var buffer bytes.Buffer
	buffer.WriteString(keyData)
	os.Mkdir(path.Join(attrs.Home, ".ssh"), 0700)
	if err := ioutil.WriteFile(keyFile, buffer.Bytes(), 0600); err != nil {
		log.Printf("Error: Can't write %s file for user %s: %s", keyFile, username, err)
	}
	// os.Chown isn't working, not sure why, use native chown instead
	exec.Command("chown", "-R", username+":"+username, path.Join(attrs.Home, ".ssh")).Run()
	return true
}

// search for a close match in a range
func inRangePattern(needle string, haystack []string) bool {
	for _, v := range haystack {
		// filepath.Match performs glob/wildcard matching
		if match, _ := filepath.Match(v, needle); match || v == needle {
			return true
		}
	}
	return false
}

func main() {
	r := gitClone(repo)
	users := gatherRepoUsers(repo, r, realm)

	for _, user := range users {
		if inRangePattern(realm, user.Realms) {
			if userExists(user.Username) || createUser(user) {
				updateUser(user)
			}
		} else if userExists(user.Username) {
			deleteUser(user.Username)
		}
	}
}
