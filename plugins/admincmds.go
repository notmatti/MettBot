package plugins

import (
	"../ircclient"
	"strings"
)

type AdminPlugin struct {
	ic *ircclient.IRCClient
}

func (q *AdminPlugin) Register(cl *ircclient.IRCClient) {
	q.ic = cl

	q.ic.RegisterCommandHandler("inviteme", 1, 400, q)
	q.ic.RegisterCommandHandler("say", 2, 500, q)
	q.ic.RegisterCommandHandler("notice", 2, 500, q)
	q.ic.RegisterCommandHandler("action", 2, 500, q)
}

func (q *AdminPlugin) String() string {
	return "admin"
}

func (q *AdminPlugin) Info() string {
	return "provides commands for bot-admins"
}

func (q *AdminPlugin) Usage(cmd string) string {
	switch cmd {
	case "inviteme":
		return "inviteme <channelname>"
	case "say":
		return "say <channelname> <message>"
	case "notice":
		return "notice <channelname> <message>"
	case "action":
		return "action <channelname> <message>"
	}
	return ""
}

func (q *AdminPlugin) ProcessLine(msg *ircclient.IRCMessage) {
	if msg.Command != "JOIN" {
		return
	}
	if q.ic.GetAccessLevel(msg.Source) >= 200 {
		q.ic.SendLine("MODE " + msg.Target + " +o " + strings.SplitN(msg.Source, "!", 2)[0])
		return
	}
}

func (q *AdminPlugin) ProcessCommand(cmd *ircclient.IRCCommand) {
	switch cmd.Command {
	case "inviteme":
		q.ic.SendLine("INVITE " + strings.SplitN(cmd.Source, "!", 2)[0] + " " + cmd.Args[0])
	case "say":
		q.ic.SendLine("PRIVMSG " + cmd.Args[0] + " :" + strings.Join(cmd.Args[1:], " "))
	case "notice":
		q.ic.SendLine("NOTICE " + cmd.Args[0] + " :" + strings.Join(cmd.Args[1:], " "))
	case "action":
		q.ic.SendLine("PRIVMSG " + cmd.Args[0] + " :\001ACTION " + strings.Join(cmd.Args[1:], " ") + "\001")
	}
}

func (q *AdminPlugin) Unregister() {
	return
}
