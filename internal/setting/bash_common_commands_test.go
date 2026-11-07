package setting

import "testing"

// TestPlanModeCommonCommands verifies that the commands most frequently used
// during planning (curl, wget, brew, npm/yarn/pnpm, tar, unzip, docker,
// kubectl, pip, and the simple inspection commands added to the flat list)
// pass the plan-mode gate when used as read-only, and are blocked when used as
// writers.
func TestPlanModeCommonCommands(t *testing.T) {
	readOnly := []string{
		// --- simple flat-list additions ---
		"awk '{print $1}' go.mod",
		"awk -F: '{print $2}' /etc/hosts",
		"man git",
		"less go.mod",
		"more README.md",
		"ps aux",
		"ps -ef",
		"pgrep san",
		"lsof -i :8080",
		"env",
		"printenv PATH",
		"locale",
		"arch",
		"sw_vers",
		"uptime",
		"cal",
		"bc",
		"expr 1 + 1",
		"type go",
		"less go.mod",
		"zcat archive.gz",
		"ping -c 1 google.com",
		"traceroute google.com",
		"nslookup google.com",
		"dig google.com",
		"host google.com",
		"whois google.com",
		"netstat -an",
		"ss -tlnp",
		"ifconfig",
		"ip addr",
		"open README.md",
		"pbpaste",
		// --- curl ---
		"curl https://api.github.com/repos/owner/repo",
		"curl -s https://registry.npmjs.org/react/latest",
		"curl -H 'Accept: application/json' https://api.example.com/v1/users",
		"curl -o - https://example.com/data.json",
		"curl --output - https://example.com/data.json",
		"curl -X GET https://api.example.com",
		"curl --request GET https://api.example.com",
		"curl -sL https://example.com | head -20",
		// --- wget ---
		"wget -q -O - https://example.com/data.json",
		"wget --quiet --output-document=- https://api.example.com",
		// --- brew ---
		"brew list",
		"brew info go",
		"brew search neovim",
		"brew deps go",
		"brew outdated",
		"brew --version",
		"brew doctor",
		"brew config",
		// --- npm / yarn / pnpm ---
		"npm list",
		"npm info react",
		"npm view lodash version",
		"npm outdated",
		"npm --version",
		"yarn list",
		"yarn info react",
		"yarn outdated",
		"yarn --version",
		"pnpm list",
		"pnpm info react",
		"pnpm --version",
		"bun --version",
		// --- pip ---
		"pip list",
		"pip show requests",
		"pip freeze",
		"pip --version",
		"pip3 list",
		"pip3 show numpy",
		// --- tar ---
		"tar -tf archive.tar.gz",
		"tar -tvf archive.tar",
		"tar --list -f archive.tar.gz",
		// --- unzip ---
		"unzip -l archive.zip",
		"unzip -v archive.zip",
		// --- docker ---
		"docker ps",
		"docker ps -a",
		"docker images",
		"docker inspect mycontainer",
		"docker logs mycontainer",
		"docker version",
		"docker info",
		"docker container ls",
		"docker image ls",
		"docker network ls",
		"docker system df",
		// --- kubectl ---
		"kubectl get pods",
		"kubectl get pods -n kube-system",
		"kubectl describe pod mypod",
		"kubectl logs mypod",
		"kubectl version",
		"kubectl api-resources",
		"kubectl config view",
		"kubectl config current-context",
		"kubectl auth can-i create pods",
		"k get pods",
	}
	for _, cmd := range readOnly {
		if !IsReadOnlyBashCommand(cmd) {
			t.Errorf("IsReadOnlyBashCommand(%q) = false, want true", cmd)
		}
	}

	notReadOnly := []string{
		// --- curl writes ---
		"curl -X POST https://api.example.com/items",
		"curl --request PUT https://api.example.com/items/1",
		"curl -d '{\"name\":\"x\"}' https://api.example.com",
		"curl --data-raw 'body' https://api.example.com",
		"curl -F 'file=@upload.txt' https://api.example.com",
		"curl -T myfile.txt https://api.example.com/upload",
		"curl --json '{}' https://api.example.com",
		"curl -o output.html https://example.com",
		"curl -O https://example.com/file.zip",
		// --- wget writes ---
		"wget https://example.com/file.zip",             // saves to disk by default
		"wget -O output.html https://example.com",        // named file
		"wget -P /tmp https://example.com/file.zip",      // directory prefix
		"wget -o wget.log https://example.com",           // log file
		"wget -r https://example.com",                    // recursive
		// --- brew writes ---
		"brew install go",
		"brew uninstall node",
		"brew upgrade",
		"brew update",
		"brew tap homebrew/cask",
		// --- npm writes ---
		"npm install react",
		"npm uninstall lodash",
		"npm run build",
		"npm publish",
		"npm config set registry https://registry.npmjs.org",
		// --- pip writes ---
		"pip install requests",
		"pip uninstall requests",
		"pip upgrade pip",
		// --- tar writes ---
		"tar -czf archive.tar.gz .",
		"tar -xzf archive.tar.gz",
		"tar -rf archive.tar newfile.txt",
		// --- unzip writes ---
		"unzip archive.zip",
		"unzip archive.zip -d /tmp",
		// --- docker writes ---
		"docker run ubuntu",
		"docker exec mycontainer bash",
		"docker rm mycontainer",
		"docker build .",
		"docker pull ubuntu",
		"docker network rm mynetwork",
		"docker system prune",
		// --- kubectl writes ---
		"kubectl apply -f deployment.yaml",
		"kubectl delete pod mypod",
		"kubectl create deployment myapp --image=nginx",
		"kubectl config set-context myctx",
		"kubectl config use-context myctx",
		"kubectl auth reconcile -f rbac.yaml",
	}
	for _, cmd := range notReadOnly {
		if IsReadOnlyBashCommand(cmd) {
			t.Errorf("IsReadOnlyBashCommand(%q) = true, want false", cmd)
		}
	}
}
