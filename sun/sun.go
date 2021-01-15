/**
      ___           ___           ___
     /  /\         /__/\         /__/\
    /  /:/_        \  \:\        \  \:\
   /  /:/ /\        \  \:\        \  \:\
  /  /:/ /::\   ___  \  \:\   _____\__\:\
 /__/:/ /:/\:\ /__/\  \__\:\ /__/::::::::\
 \  \:\/:/~/:/ \  \:\ /  /:/ \  \:\~~\~~\/
  \  \::/ /:/   \  \:\  /:/   \  \:\  ~~~
   \__\/ /:/     \  \:\/:/     \  \:\
     /__/:/       \  \::/       \  \:\
     \__\/         \__\/         \__\/

MIT License

Copyright (c) 2020 Jviguy

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
*/

package sun

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"github.com/sandertv/gophertunnel/minecraft/text"
	"go.uber.org/atomic"
	"log"
	"net"
	"sync"
	"time"
)

var emptychunk = make([]byte, 257)

type Sun struct {
	Listener  *minecraft.Listener
	Rays      map[string]*Ray
	Hub       IpAddr
	Planets   map[uuid.UUID]*Planet
	PListener net.Listener
	Status    StatusProvider
	Key string
	PWarnings map[string]int
	PCooldowns map[string]time.Time
}

type StatusProvider struct {
	ogs     minecraft.ServerStatus
	playerc *atomic.Int64
}

func (s StatusProvider) ServerStatus(_ int, _ int) minecraft.ServerStatus {
	return minecraft.ServerStatus{
		ServerName:  s.ogs.ServerName,
		PlayerCount: int(s.playerc.Load()),
		MaxPlayers:  s.ogs.MaxPlayers,
		ShowVersion: s.ogs.ShowVersion,
	}
}

/*
Returns a new sun with config the specified config hence W
*/
func NewSunW(config Config) (*Sun, error) {
	status := StatusProvider{config.Status, atomic.NewInt64(0)}
	listener, err := minecraft.ListenConfig{
		AuthenticationDisabled: !config.Proxy.XboxAuthentication,
		StatusProvider:         status,
		ResourcePacks:          LoadResourcePacks("./resource_packs"),
	}.Listen("raknet", fmt.Sprint(":", config.Proxy.Port))
	if err != nil {
		return nil, err
	}
	if config.Tcp.Enabled {
		plistener, err := net.Listen("tcp", ":42069")
		if err != nil {
			return nil, err
		}
		registerPackets()
		return &Sun{Listener: listener,
			PListener: plistener,
			PCooldowns: make(map[string]time.Time),
			PWarnings: make(map[string]int),
			Status:    status,
			Rays: make(map[string]*Ray,
				config.Status.MaxPlayers),
			Hub: config.Hub, Planets: make(map[uuid.UUID]*Planet),
			Key: config.Tcp.Key}, nil
	}
	registerPackets()
	return &Sun{Listener: listener,
		Status:    status,
		Rays: make(map[string]*Ray,
			config.Status.MaxPlayers),
		Hub: config.Hub, Planets: make(map[uuid.UUID]*Planet)}, nil
}

func registerPackets() {
	packet.Register(IDRayTransfer, func() packet.Packet { return &Transfer{} })
	packet.Register(IDRayText, func() packet.Packet { return &Text{} })
}

/*
Returns a new sun with a auto detected config
*/
func NewSun() (*Sun, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	return NewSunW(cfg)
}

func (s *Sun) main() {
	defer s.Listener.Close()
	if s.PListener != nil {
		go func() {
			for {
				conn, err := s.PListener.Accept()
				if err != nil {
					log.Println(err)
					continue
				}
				pl := &Planet{conn: conn}
				if tl, ok := s.PCooldowns[pl.conn.RemoteAddr().String()]; ok {
					if time.Now().Before(tl) {
						_ = pl.WritePacket(&PlanetDisconnect{Message: fmt.Sprintf("You are on cooldown for %v seconds!", time.Now().Sub(s.PCooldowns[pl.conn.RemoteAddr().String()]).Seconds())})
						_ = pl.conn.Close()
						continue
					}
					delete(s.PCooldowns, conn.RemoteAddr().String())
				}
				pk, err := pl.ReadPacket()
				if pk, ok := pk.(*PlanetAuth); ok {
					if pk.Key == s.Key {
						s.AddPlanet(pl)
						continue
					}
				}
				if _, ok := s.PWarnings[pl.conn.RemoteAddr().String()]; !ok {
					s.PWarnings[pl.conn.RemoteAddr().String()] = 3
				}
				s.PWarnings[pl.conn.RemoteAddr().String()]--
				if s.PWarnings[pl.conn.RemoteAddr().String()] <= 0 {
					s.PWarnings[pl.conn.RemoteAddr().String()] = 3
					s.PCooldowns[pl.conn.RemoteAddr().String()] = time.Now().Add(300 * time.Second)
					_ = pl.WritePacket(&PlanetDisconnect{Message: fmt.Sprintf("You are on cooldown for %v seconds!", time.Now().Sub(s.PCooldowns[pl.conn.RemoteAddr().String()]).Seconds())})
					_ = pl.conn.Close()
				}
				_ = pl.WritePacket(&PlanetDisconnect{Message: fmt.Sprintf("Invalid Authorization Key Provided %v Tries Remain Until A 300 Second Cooldown!", s.PWarnings[pl.conn.RemoteAddr().String()])})
				continue
			}
		}()
	}
	for {
		//Listener won't be closed unless it is manually done
		conn, err := s.Listener.Accept()
		if err != nil {
			log.Println(err)
			continue
		}
		ray := &Ray{conn: conn.(*minecraft.Conn)}
		rconn, err := minecraft.Dialer{
			ClientData:   ray.conn.ClientData(),
			IdentityData: ray.conn.IdentityData()}.Dial("raknet", s.Hub.ToString())
		if err != nil {
			log.Println(err)
			_ = s.Listener.Disconnect(conn.(*minecraft.Conn),
				text.Colourf("<red>You Have been Disconnected!</red>"))
			continue
		}
		ray.remoteMu.Lock()
		ray.remote = &Remote{conn: rconn, addr: s.Hub}
		ray.remoteMu.Unlock()
		s.MakeRay(ray)
	}
}

/*
Starts the proxy.
*/
func (s *Sun) Start() {
	s.main()
}

/*
Adds a player to the sun and readies them
*/
func (s *Sun) MakeRay(ray *Ray) {
	//start the player up
	var g sync.WaitGroup
	g.Add(2)
	go func() {
		if err := ray.conn.StartGame(ray.Remote().conn.GameData()); err != nil {
			return // Connection was closed by server
		}
		g.Done()
	}()
	go func() {
		if err := ray.Remote().conn.DoSpawn(); err != nil {
			return // Connection was closed by server
		}
		g.Done()
	}()
	g.Wait()
	//start translator
	ray.initTranslators(ray.conn.GameData())
	//Add to player count
	s.Status.playerc.Add(1)
	//add to player list
	s.Rays[ray.conn.IdentityData().Identity] = ray
	//Start the two listener functions
	s.handleRay(ray)
}

/*
Closes a players session cleanly with a nice disconnection message!
*/
func (s *Sun) BreakRay(ray *Ray) {
	_ = s.Listener.Disconnect(ray.conn, text.Colourf("<red>You Have been Disconnected!</red>"))
	_ = ray.Remote().conn.Close()
	s.Status.playerc.Dec()
	delete(s.Rays, ray.conn.IdentityData().Identity)
}

func (s *Sun) SendMessageToServers(Message string, Servers []string) {
	for _, server := range Servers {
		for _, ray := range s.Rays {
			if ray.Remote().Addr().ToString() == server {
				_ = ray.conn.WritePacket(&packet.Text{Message: Message, TextType: packet.TextTypeRaw})
			}
		}
	}
}

/*
SendMessage is used for sending a Sun wide message to all the connected clients
*/
func (s *Sun) SendMessage(Message string) {
	for _, ray := range s.Rays {
		//Send raw chat to each player as client will accept it
		_ = ray.conn.WritePacket(&packet.Text{Message: Message, TextType: packet.TextTypeRaw})
	}
}

func (s *Sun) AddPlanet(planet *Planet) {
	id := uuid.New()
	planet.id = id
	s.Planets[id] = planet
	s.handlePlanet(planet)
}
