package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"plant/stack"
	"strings"

	"github.com/acarl005/stripansi"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type process struct {
	Cmd    *exec.Cmd
	Stdin  io.Writer
	Stdout io.Reader
}

func main() {
	var in io.Reader
	var tfProc *process
	tfCommand := os.Args[1:]
	if len(tfCommand) > 0 {
		proc, err := runTerraform(tfCommand)
		if err != nil {
			panic(err)
		}
		tfProc = proc
		in = tfProc.Stdout
	} else {
		in = os.Stdin
	}

	root := newTreeNode("Terraform plan")
	query, err := readTree(root, in)
	if err != nil {
		panic(err)
	}

	tree := newTreeView(root)
	app := newApp(tree)

	if tfProc != nil {
		// using `SetAfterDrawFunc` to ensure `app.Stop` is not called before `app.Run`
		app.SetAfterDrawFunc(func(_ tcell.Screen) {
			app.SetAfterDrawFunc(nil)
			go func() {
				err := tfProc.Cmd.Wait()
				if err != nil || query != "" {
					app.Stop()
				}
			}()
		})
	}

	queryAnswered := false
	if query != "" {
		if tfProc != nil {
			setupInputDialog(app, tree, query, tfProc.Stdin, func() { queryAnswered = true })
		} else {
			if _, err := io.Copy(os.Stdout, in); err != nil {
				panic(err)
			}
			fmt.Fprintln(os.Stderr, "plant: Piping only works with `terraform plan | plant`. For apply or destroy run `plant terraform apply` or `plant terraform destroy`.")
			os.Exit(1)
		}
	}

	if err := app.Run(); err != nil {
		panic(err)
	}

	if tfProc != nil && !queryAnswered {
		tfProc.Cmd.Process.Signal(os.Interrupt)
	}

	// print further Terraform output
	if _, err := io.Copy(os.Stdout, in); err != nil && !errors.Is(err, os.ErrClosed) {
		panic(err)
	}
}

func runTerraform(command []string) (*process, error) {
	cmd := exec.Command(command[0], command[1:]...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create StdinPipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create StdoutPipe: %w", err)
	}

	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to exec command %s: %w", command, err)
	}

	return &process{
		Cmd:    cmd,
		Stdin:  stdin,
		Stdout: stdout,
	}, nil
}

func readTree(root *tview.TreeNode, in io.Reader) (string, error) {
	parentStack := stack.New[*tview.TreeNode]()
	parentStack.Push(root)

	start := false
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		coloredLine := scanner.Text()
		fmt.Fprintln(os.Stdout, coloredLine)
		rawLine := stripansi.Strip(coloredLine)
		if !start {
			if isStart(rawLine) {
				start = true
			} else {
				continue
			}
		}
		if needsInput(rawLine) {
			return rawLine, nil
		}

		node := newTreeNode(ansiColorToTview(coloredLine)).Collapse()
		parent := parentStack.MustPeek()
		parent.AddChild(node)
		node.SetReference(parent)

		opener := isOpener(rawLine)
		closer := isCloser(rawLine)
		node.SetSelectable(opener)
		if opener {
			node.SetSelectable(true)
			parentStack.Push(node)
			updateSuffix(node)
		} else if closer {
			parentStack.Pop()
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", nil
}

func isStart(line string) bool {
	return strings.HasSuffix(line, "Objects have changed outside of Terraform") ||
		strings.HasPrefix(line, "Terraform detected the following changes") ||
		strings.HasPrefix(line, "Terraform used the selected providers") ||
		strings.HasPrefix(line, "Terraform will perform the following actions")
}

func needsInput(line string) bool {
	return line == "Do you want to perform these actions?" ||
		line == "Do you really want to destroy all resources?"
}

func isOpener(line string) bool {
	if line == "" {
		return false
	}
	lastChar := line[len(line)-1]
	return lastChar == '(' || lastChar == '[' || lastChar == '{'
}

func isCloser(line string) bool {
	trimmedLine := strings.TrimSpace(line)
	if trimmedLine == "" {
		return false
	}
	firstChar := trimmedLine[0]
	return firstChar == ')' || firstChar == ']' || firstChar == '}'
}

func ansiColorToTview(line string) string {
	replacer := strings.NewReplacer(
		"\033[30m", "[black]",
		"\033[31m", "[red]",
		"\033[32m", "[green]",
		"\033[33m", "[yellow]",
		"\033[34m", "[blue]",
		"\033[35m", "[magenta]",
		"\033[36m", "[cyan]",
		"\033[37m", "[white]",
		"\033[90m", "[gray]",
		"\033[91m", "[red]",
		"\033[92m", "[green]",
		"\033[93m", "[yellow]",
		"\033[94m", "[blue]",
		"\033[95m", "[magenta]",
		"\033[96m", "[cyan]",
		"\033[97m", "[white]",
		"\033[1m", "[::b]", // bold
		"\033[3m", "[::i]", // italic
		"\033[4m", "[::u]", // underline
		"\033[0m", "[-:-:-]", // reset all
	)
	return replacer.Replace(line)
}

func setupInputDialog(app *tview.Application, tree *tview.TreeView, query string, tfIn io.Writer, done func()) {
	inputNode := newTreeNode(query).
		SetSelectable(true).
		SetSelectedFunc(func() {
			modal := tview.NewModal().
				SetText(query).
				AddButtons(dialogButtons()).
				SetDoneFunc(func(buttonIndex int, buttonLabel string) {
					fmt.Fprintln(tfIn, buttonLabel)
					done()
					app.Stop()
				})
			modal.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
				if event.Key() == tcell.KeyEsc {
					app.SetRoot(tree, true)
					return nil
				}
				return event
			})
			app.SetRoot(modal, true)
		})
	tree.GetRoot().AddChild(inputNode)
}

func dialogButtons() []string {
	const no = "no"
	const yes = "yes"
	buttons := []string{no, no, no, yes}
	shuffleSlice(buttons[1:])
	return buttons
}

func shuffleSlice[T any](slice []T) {
	rand.Shuffle(len(slice), func(i, j int) {
		slice[i], slice[j] = slice[j], slice[i]
	})
}

func newTreeNode(text string) *tview.TreeNode {
	return tview.NewTreeNode(text).SetTextStyle(tcell.StyleDefault)
}

func newTreeView(root *tview.TreeNode) *tview.TreeView {
	tree := tview.NewTreeView().
		SetRoot(root).
		SetTopLevel(1). // hide root node
		SetGraphics(false).
		SetAlign(true)

	tree.SetBackgroundColor(tcell.ColorDefault)
	tree.SetCurrentNode(firstSelectableNode(root))
	setupInputCapture(tree)

	return tree
}

func newApp(tree *tview.TreeView) *tview.Application {
	return tview.NewApplication().
		SetRoot(tree, true).
		EnableMouse(true)
}

func firstSelectableNode(root *tview.TreeNode) *tview.TreeNode {
	for _, child := range root.GetChildren() {
		if len(child.GetChildren()) > 0 {
			return child
		}
	}
	return nil
}

func setupInputCapture(tree *tview.TreeView) {
	tree.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		node := tree.GetCurrentNode()
		if node == nil {
			return event
		}

		switch event.Key() {
		case tcell.KeyRight:
			if node.IsExpanded() {
				tree.Move(1)
			} else {
				node.SetExpanded(true)
				updateSuffix(node)
			}
			return nil
		case tcell.KeyLeft:
			if node.IsExpanded() {
				node.SetExpanded(false)
				updateSuffix(node)
			} else {
				parent, ok := node.GetReference().(*tview.TreeNode)
				if ok && parent != tree.GetRoot() {
					tree.SetCurrentNode(parent)
				}
			}
			return nil
		default:
			return event
		}
	})

	tree.SetSelectedFunc(func(node *tview.TreeNode) {
		node.SetExpanded(!node.IsExpanded())
		updateSuffix(node)
	})
}

func updateSuffix(node *tview.TreeNode) {
	if node.IsExpanded() {
		node.SetText(getExpandedText(node.GetText()))
	} else {
		node.SetText(getCollapsedText(node.GetText()))
	}
}

func getCollapsedText(expandedText string) string {
	if strings.HasSuffix(expandedText, "(") {
		return expandedText + "...)"
	} else if strings.HasSuffix(expandedText, "[") {
		return expandedText + "...]"
	} else if strings.HasSuffix(expandedText, "{") {
		return expandedText + "...}"
	} else {
		return expandedText
	}
}

func getExpandedText(collapsedText string) string {
	collapsedSuffixes := []string{
		"(...)",
		"[...]",
		"{...}",
	}

	for _, collapsedSuffix := range collapsedSuffixes {
		if strings.HasSuffix(collapsedText, collapsedSuffix) {
			cutLen := len(collapsedSuffix) - 1
			return collapsedText[:len(collapsedText)-cutLen]
		}
	}

	return collapsedText
}
