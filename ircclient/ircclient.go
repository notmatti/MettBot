// Package ircclient provides the main interface for library users
// It manages a single connection to the server and the associated
// configuration and plugins.
package ircclient

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

type IRCClient struct {
	conn       *ircConn
	plugins    map[string]Plugin
	handlers   map[string]handler
	disconnect chan bool
}

type handler struct {
	Handler   Plugin
	Command   string
	Minparams int
	Minaccess int
}

// Returns a new IRCClient connection with the given configuration options.
// It will not connect to the given server until Connect() has been called,
// so you can register plugins before connecting
func NewIRCClient(configfile string) *IRCClient {
	c := &IRCClient{nil, make(map[string]Plugin), make(map[string]handler), make(chan bool)}
	c.RegisterPlugin(&basicProtocol{})
	c.RegisterPlugin(NewConfigPlugin(configfile))
	c.RegisterPlugin(new(authPlugin))
	return c
}

// Registers a new plugin. Plugins can be registered at any time, even before
// the actual connection attempt. The plugin's Unregister() function will already
// be called when the connection is lost.
func (ic *IRCClient) RegisterPlugin(p Plugin) error {
	if _, ok := ic.plugins[p.String()]; ok == true {
		return errors.New("Plugin already exists")
	}
	p.Register(ic)
	ic.plugins[p.String()] = p
	return nil
}

// Registers a command handler. Plugin callbacks will only be called if
// the command matches. Note that only a single plugin per command may
// be registered. This function is not synchronized, e.g., it shall only
// be called during registration (as Plugin.Register()-calls are currently
// sequential).
func (ic *IRCClient) RegisterCommandHandler(command string, minparams int, minaccess int, plugin Plugin) error {
	if plug, err := ic.handlers[command]; err {
		return errors.New("Handler is already registered by plugin: " + plug.Handler.String())
	}
	ic.handlers[command] = handler{plugin, command, minparams, minaccess}
	return nil
}

// Gets one of the configuration options stored in the config object. Valid config
// options for section "Server" usually include:
//  - nick
//  - hostport (colon-seperated host and port to connect to)
//  - realname (the real name)
//  - ident
//  - trigger
// All other sections are managed by the library user. Returns an
// empty string if the option is empty, this means: you currently can't
// use empty config values - they will be deemed non-existent!
func (ic *IRCClient) GetStringOption(section, option string) string {
	c := ic.plugins["conf"]
	cf, _ := c.(*ConfigPlugin)
	cf.Lock()
	retval, _ := cf.Conf.String(section, option)
	cf.Unlock()
	return retval
}

// Sets a single config option. Existing parameters are overriden,
// if necessary, a new config section is automatically added.
func (ic *IRCClient) SetStringOption(section, option, value string) {
	c := ic.plugins["conf"]
	cf, _ := c.(*ConfigPlugin)
	cf.Lock()
	if !cf.Conf.HasSection(section) {
		cf.Conf.AddSection(section)
	}
	if cf.Conf.HasOption(section, option) {
		cf.Conf.RemoveOption(section, option)
	}
	cf.Conf.AddOption(section, option, value)
	cf.Unlock()
}

// Removes a single config option. Note: This does not delete the section,
// even if it's empty.
func (ic *IRCClient) RemoveOption(section, option string) {
	c := ic.plugins["conf"]
	cf, _ := c.(*ConfigPlugin)
	cf.Lock()
	defer cf.Unlock()

	if !cf.Conf.HasSection(section) {
		// nothing to do
		return
	}
	cf.Conf.RemoveOption(section, option)
}

// Gets a list of all config keys for a given section. The return value is
// an empty slice if there are no options present _or_ if there is no
// section present. There is currently no way to check whether a section
// exists, it is automatically added when calling one of the SetOption()
// methods.
func (ic *IRCClient) GetOptions(section string) []string {
	c := ic.plugins["conf"]
	cf, _ := c.(*ConfigPlugin)
	cf.Lock()
	defer cf.Unlock()
	opts, err := cf.Conf.Options(section)
	if err != nil {
		return []string{}
	}
	return opts
}

// Does the same as GetStringOption(), but with integers. Returns an os.Error,
// if the given config option does not exist.
func (ic *IRCClient) GetIntOption(section, option string) (int, error) {
	c := ic.plugins["conf"]
	cf, _ := c.(*ConfigPlugin)
	cf.Lock()
	defer cf.Unlock()
	v, err := cf.Conf.Int(section, option)
	if err != nil {
		return -1, err
	}
	return v, nil
}

// See SetStringOption()
func (ic *IRCClient) SetIntOption(section, option string, value int) {
	c := ic.plugins["conf"]
	cf, _ := c.(*ConfigPlugin)
	cf.Lock()
	defer cf.Unlock()
	stropt := fmt.Sprintf("%d", value)
	if !cf.Conf.HasSection(section) {
		cf.Conf.AddSection(section)
	}
	cf.Conf.AddOption(section, option, stropt)
}

// Gets the highest matching access level for a given hostmask by comparing
// the mask against all authorization entries. Default return value is 0
// (no access).
func (ic *IRCClient) GetAccessLevel(host string) int {
	a := ic.plugins["auth"]
	auth, _ := a.(*authPlugin)
	return auth.GetAccessLevel(host)
}

// Sets the access level for the given hostmask to level. Note that host may
// be a regular expression, if exactly the same expression is already present
// in the database, it is overridden.
func (ic *IRCClient) SetAccessLevel(host string, level int) {
	a := ic.plugins["auth"]
	auth, _ := a.(*authPlugin)
	auth.SetAccessLevel(host, level)
}

// Delete the given regular expression from auth database. The "host" parameter
// has to be exactly the string stored in the database, otherwise, the command
// will have no effect.
func (ic *IRCClient) DelAccessLevel(host string) {
	a := ic.plugins["auth"]
	auth, _ := a.(*authPlugin)
	auth.DelAccessLevel(host)
}

// Connects to the server specified on object creation. If the chosen nickname is
// already in use, it will automatically be suffixed with an single underscore until
// an unused nickname is found. This function blocks until the connection attempt
// has been finished.
func (ic *IRCClient) Connect() error {
	ic.conn = NewircConn()
	e := ic.conn.Connect(ic.GetStringOption("Server", "host"))
	if e != nil {
		return e
	}

	// Doing bot online restart. Don't reregister.
	if len(os.Args) > 1 {
		return nil
	}

	ic.conn.Output <- "NICK " + ic.GetStringOption("Server", "nick")
	ic.conn.Output <- "USER " + ic.GetStringOption("Server", "ident") + " * Q :" + ic.GetStringOption("Server", "realname")
	nick := ic.GetStringOption("Server", "nick")

	for {
		line, ok := <-ic.conn.Input
		if !ok {
			return <-ic.conn.Err
		}

		// Invoke plugin line handlers.
		// At this point, it makes no sense to
		// process "commands". If a plugin needs
		// interaction in this state, it should be
		// low-level.
		s := ParseServerLine(line)
		if s == nil {
			continue
		}
		for _, p := range ic.plugins {
			go p.ProcessLine(s)
		}

		switch s.Command {
		case "433":
			// Nickname already in use
			nick = nick + "_"
			ic.SetStringOption("Server", "nick", nick)
			ic.conn.Output <- "NICK " + nick
		case "001":
			// Successfully registered
			return nil
		}
	}
	return nil
}

func (ic *IRCClient) dispatchHandlers(in string) {
	var c *IRCCommand = nil

	s := ParseServerLine(in)
	if s == nil {
		return
	}

	// Call line handlers
	for _, p := range ic.plugins {
		go p.ProcessLine(s)
	}

	if (s.Command != "PRIVMSG" && s.Command != "NOTICE") || strings.Index(s.Args[0], ic.GetStringOption("Server", "trigger")) != 0 {
		return
	}

	c = ParseCommand(s)
	if c == nil || len(c.Command) == 0 {
		return
	}

	// Strip trigger
	c.Command = c.Command[len(ic.GetStringOption("Server", "trigger")):]

	// Call command handler
	handler, ok := ic.handlers[c.Command]
	if !ok {
		return
	}

	// Don't do regexp matching, if we don't need access anyway
	if handler.Minaccess > 0 && ic.GetAccessLevel(c.Source) < handler.Minaccess {
		ic.Reply(c, "You are not authorized to do that.")
		return
	}
	if len(c.Args) < handler.Minparams {
		//ic.Reply(c, "This command requires at least "+fmt.Sprintf("%d", handler.Minparams)+" parameters")
		ic.Reply(c, ic.GetUsage(c.Command))
		return
	}
	go handler.Handler.ProcessCommand(c)
}

// Starts the actual command processing. This function will block until the connection
// has either been lost or Disconnect() has been called (by a plugin or by the library
// user).
func (ic *IRCClient) InputLoop() error {
	for {
		in, ok := <-ic.conn.Input
		if !ok {
			return <-ic.conn.Err
		}
		ic.dispatchHandlers(in)
	}
	panic("This never happens")
}

// Disconnects from the server with the given quit message. All plugins wil be unregistered
// and pending messages in queue (e.g. because of floodprotection) will be flushed. This will
// also make InputLoop() return.
func (ic *IRCClient) Disconnect(quitmsg string) {
	ic.Shutdown()
	ic.conn.Output <- "QUIT :" + quitmsg
	ic.conn.Quit()
}

// Dumps a raw line to the server socket. This is usually called by plugins, but may also
// be used by the library user.
func (ic *IRCClient) SendLine(line string) {
	line = strings.Replace(line, "\r", " ", -1)
	line = strings.Replace(line, "\n", " ", -1) // remove newlines
	// cut line, so we won't hit the 512 chars limit ("\r\n" will be appended)
	if len(line) > 510 {
		line = line[:510]
	}
	ic.conn.Output <- line
}

func (ic *IRCClient) Shutdown() {
	for _, p := range ic.plugins {
		p.Unregister()
	}
}

// Returns a channel on which all command handlers will be sent.
func (ic *IRCClient) IterHandlers() <-chan handler {
	ch := make(chan handler, len(ic.handlers))
	go func() {
		for _, e := range ic.handlers {
			ch <- e
		}
		close(ch)
	}()
	return ch
}

// Get the pointer to a specific plugin that has been registered using RegisterPlugin()
// Name is the name the plugin identifies itself with when String() is called on it.
func (ic *IRCClient) GetPlugin(name string) Plugin {
	return ic.plugins[name]
}

// Get the Usage string from the Plugin that has registered itself as handler for
// the Command cmd. we need to wrap this to ircclient because the handlers are not
// public, and GetPlugin doesn't help us either, because the plugin<->command mapping
// is not known
func (ic *IRCClient) GetUsage(cmd string) string {
	plugin, exists := ic.handlers[cmd]
	if !exists {
		return "no such command"
	}
	return plugin.Handler.Usage(cmd)
}

// Sends a reply to a parsed message from a user. This is mostly intended for plugins
// and will automatically distinguish between channel and query messages. Note: Notice
// replies will currently be sent to the client using PRIVMSG, this may change in the
// future.
func (ic *IRCClient) Reply(cmd *IRCCommand, message string) {
	var target string
	if cmd.Target != ic.GetStringOption("Server", "nick") {
		target = cmd.Target
	} else {
		target = strings.SplitN(cmd.Source, "!", 2)[0]
	}
	ic.SendLine("NOTICE " + target + " :" + message)
}
func (ic *IRCClient) ReplyMsg(msg *IRCMessage, message string) {
	var target string
	if msg.Target != ic.GetStringOption("Server", "nick") {
		target = msg.Target
	} else {
		target = strings.SplitN(msg.Source, "!", 2)[0]
	}
	ic.SendLine("NOTICE " + target + " :" + message)
}

// Returns socket fd. Needed for kexec
func (ic *IRCClient) GetSocket() int {
	return ic.conn.GetSocket()
}

func (ic *IRCClient) GetPlugins() map[string]Plugin {
	return ic.plugins
}
