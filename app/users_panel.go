package main

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"autoipo/app/internal/format"
	"autoipo/internal/paths"
	"autoipo/internal/users"
)

// usersPanel is the Users tab: a searchable list of bank-portal users with
// per-row reveal/copy/edit/delete. It is read-only when the users file is
// corrupt (ctx.users == nil).
type usersPanel struct {
	ctx      *appContext
	records  []users.Record // cached from the store; UI thread only
	revealed map[string]bool
	search   *widget.Entry
	rows     *fyne.Container
	addBtn   *widget.Button
	summary  *widget.Label
	root     fyne.CanvasObject
}

func newUsersPanel(ctx *appContext) *usersPanel {
	p := &usersPanel{ctx: ctx, revealed: map[string]bool{}, rows: container.NewVBox()}
	p.search = hintEntry("Search by name, PAN or user ID…")
	p.search.OnChanged = func(string) { p.render() }
	p.addBtn = widget.NewButton("Add user", func() { p.showEditor(users.Record{}, "") })
	p.addBtn.Importance = widget.HighImportance
	p.summary = wrappedLabel("")
	header := container.NewBorder(nil, nil, nil, p.addBtn, p.search)
	p.root = container.NewBorder(container.NewVBox(header, p.summary), nil, nil, nil, container.NewVScroll(p.rows))
	if ctx.users == nil {
		p.addBtn.Disable()
		p.summary.SetText("Read-only: the users file " + paths.UsersFile() + " is corrupt. Fix or remove it and restart the app.")
	}
	p.render()
	return p
}

// reload fetches the records off the UI thread and re-renders. The first
// call must happen once the Fyne event loop is running.
func (p *usersPanel) reload() {
	p.ctx.listUsers(func(records []users.Record) {
		p.records = records
		p.render()
	})
}

func (p *usersPanel) render() {
	query := strings.ToLower(strings.TrimSpace(p.search.Text))
	p.rows.Objects = nil
	shown := 0
	for _, record := range p.records {
		if query != "" && !matchesUser(record, query) {
			continue
		}
		p.rows.Add(p.row(record))
		shown++
	}
	if shown == 0 {
		if len(p.records) == 0 {
			p.rows.Add(widget.NewLabel("No users yet. Add one to run flows with its credentials."))
		} else {
			p.rows.Add(widget.NewLabel("No users match the search."))
		}
	}
	p.rows.Refresh()
	if p.ctx.users != nil {
		p.summary.SetText(fmt.Sprintf("%d user(s) · passwords are kept in the %s secrets store", len(p.records), p.ctx.secrets.Backend()))
	}
}

func matchesUser(record users.Record, query string) bool {
	return strings.Contains(strings.ToLower(record.Name), query) ||
		strings.Contains(strings.ToLower(record.PAN), query) ||
		strings.Contains(strings.ToLower(record.UserID), query)
}

func (p *usersPanel) row(record users.Record) fyne.CanvasObject {
	revealed := p.revealed[record.PAN]
	pan, userID, password := format.MaskPAN(record.PAN), format.MaskUserID(record.UserID), users.MaskedPassword(record.Password)
	if revealed {
		pan, userID, password = record.PAN, record.UserID, record.Password
		if password == "" {
			password = "Not set"
		}
	}
	details := container.NewVBox(
		boldLabel(record.Name),
		widget.NewLabel(fmt.Sprintf("%s · %s · ID %s · %s", record.BankName, pan, userID, password)),
	)
	revealLabel := "Reveal"
	if revealed {
		revealLabel = "Hide"
	}
	reveal := widget.NewButton(revealLabel, func() {
		p.revealed[record.PAN] = !p.revealed[record.PAN]
		p.render()
	})
	copyID := widget.NewButton("Copy ID", func() {
		p.ctx.app.Clipboard().SetContent(record.UserID)
		p.ctx.logEvent("copied the user ID of %s to the clipboard", record.Name)
	})
	copyPassword := widget.NewButton("Copy password", func() {
		p.ctx.app.Clipboard().SetContent(record.Password)
		p.ctx.logEvent("copied the password of %s to the clipboard", record.Name)
	})
	edit := widget.NewButton("Edit", func() { p.showEditor(record, record.PAN) })
	remove := widget.NewButton("Delete", func() { p.confirmDelete(record) })
	if p.ctx.users == nil {
		edit.Disable()
		remove.Disable()
	}
	buttons := container.NewVBox(container.NewHBox(reveal, copyID, copyPassword, edit, remove))
	return container.NewVBox(container.NewBorder(nil, nil, nil, buttons, details), widget.NewSeparator())
}

// showEditor opens the add/edit form. previousPAN is empty for a new user.
func (p *usersPanel) showEditor(draft users.Record, previousPAN string) {
	if p.ctx.users == nil {
		return
	}
	name := hintEntry("Full name")
	name.SetText(draft.Name)
	pan := hintEntry("ABCDE1234F")
	pan.SetText(draft.PAN)
	bank := widget.NewSelect(users.SupportedBanks, nil)
	if normalized := users.NormalizeBank(draft.BankName); normalized != "" {
		bank.SetSelected(normalized)
	} else {
		bank.SetSelected(users.SupportedBanks[0])
	}
	userID := hintEntry("Portal login ID")
	userID.SetText(draft.UserID)
	password := widget.NewPasswordEntry()
	password.SetText(draft.Password)
	email := hintEntry("optional")
	email.SetText(draft.Email)
	phone := hintEntry("optional")
	phone.SetText(draft.Phone)

	title, confirm := "Add user", "Save user"
	if previousPAN != "" {
		title, confirm = "Edit user", "Update user"
	}
	items := []*widget.FormItem{
		{Text: "Name", Widget: name},
		{Text: "PAN", Widget: pan},
		{Text: "Bank", Widget: bank},
		{Text: "User ID", Widget: userID},
		{Text: "Password", Widget: password},
		{Text: "Email", Widget: email},
		{Text: "Phone", Widget: phone},
	}
	form := dialog.NewForm(title, confirm, "Cancel", items, func(ok bool) {
		if !ok {
			return
		}
		record := users.Record{
			Name: name.Text, PAN: pan.Text, BankName: bank.Selected, UserID: userID.Text,
			Password: password.Text, Email: email.Text, Phone: phone.Text,
		}
		if err := record.Validate(); err != nil {
			// Show the problem, then reopen the form with the entered values.
			problem := dialog.NewError(err, p.ctx.window)
			problem.SetOnClosed(func() { p.showEditor(record, previousPAN) })
			problem.Show()
			return
		}
		p.save(record, previousPAN)
	}, p.ctx.window)
	form.Resize(fyne.NewSize(dialogWidth, form.MinSize().Height))
	form.Show()
}

// save writes the record off the UI thread (the secrets backend may spawn a
// process) and refreshes the list.
func (p *usersPanel) save(record users.Record, previousPAN string) {
	store := p.ctx.users
	go func() {
		err := store.Put(record, previousPAN)
		fyne.Do(func() {
			if err != nil {
				p.ctx.logEvent("save user failed: %v", err)
				dialog.ShowError(err, p.ctx.window)
				return
			}
			p.ctx.logEvent("saved user %s", record.Name)
			p.reload()
		})
	}()
}

func (p *usersPanel) confirmDelete(record users.Record) {
	message := fmt.Sprintf("Remove %s (%s) from the local registry? The stored password is deleted too.", record.Name, format.MaskPAN(record.PAN))
	dialog.ShowConfirm("Delete user", message, func(ok bool) {
		if !ok {
			return
		}
		store := p.ctx.users
		go func() {
			err := store.Delete(record.PAN)
			fyne.Do(func() {
				if err != nil {
					p.ctx.logEvent("delete user failed: %v", err)
					dialog.ShowError(err, p.ctx.window)
					return
				}
				delete(p.revealed, record.PAN)
				p.ctx.logEvent("deleted user %s", record.Name)
				p.reload()
			})
		}()
	}, p.ctx.window)
}
