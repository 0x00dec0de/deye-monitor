package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// SolarmanClient talks to a Solarman (IGEN) LSW-3 WiFi logger using the V5
// framing protocol on TCP 8899 and tunnels Modbus RTU requests to the inverter.
type SolarmanClient struct {
	addr         string
	loggerSerial uint32
	slaveID      byte
	timeout      time.Duration
	seq          byte
}

func NewSolarmanClient(addr string, loggerSerial uint32, slaveID byte, timeout time.Duration) *SolarmanClient {
	return &SolarmanClient{addr: addr, loggerSerial: loggerSerial, slaveID: slaveID, timeout: timeout}
}

// ReadHoldingRegisters opens a fresh TCP connection, sends one Modbus
// function 03 request through the logger and returns the register values.
func (c *SolarmanClient) ReadHoldingRegisters(start, count uint16) ([]uint16, error) {
	if count == 0 || count > 125 {
		return nil, fmt.Errorf("invalid register count %d", count)
	}
	conn, err := net.DialTimeout("tcp", c.addr, c.timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(c.timeout))

	c.seq++
	req := c.buildFrame(c.seq, modbusReadRequest(c.slaveID, start, count))
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}

	// The logger may interleave unsolicited frames (heartbeats, data pushes);
	// keep reading until we get the response to our sequence number.
	for attempt := 0; attempt < 8; attempt++ {
		frame, err := readV5Frame(conn)
		if err != nil {
			return nil, err
		}
		if frame[3] != 0x10 || frame[4] != 0x15 || frame[5] != c.seq {
			continue
		}
		return parseModbusResponse(frame, c.slaveID, count)
	}
	return nil, errors.New("no matching response from logger")
}

func (c *SolarmanClient) buildFrame(seq byte, modbus []byte) []byte {
	payload := make([]byte, 0, 15+len(modbus))
	payload = append(payload, 0x02)       // frame type
	payload = append(payload, 0x00, 0x00) // sensor type
	payload = append(payload, 0, 0, 0, 0) // total working time
	payload = append(payload, 0, 0, 0, 0) // power on time
	payload = append(payload, 0, 0, 0, 0) // offset time
	payload = append(payload, modbus...)

	frame := make([]byte, 0, 11+len(payload)+2)
	frame = append(frame, 0xA5)
	frame = binary.LittleEndian.AppendUint16(frame, uint16(len(payload)))
	frame = append(frame, 0x10, 0x45) // control code 0x4510
	frame = append(frame, seq, 0x00)
	frame = binary.LittleEndian.AppendUint32(frame, c.loggerSerial)
	frame = append(frame, payload...)
	frame = append(frame, v5Checksum(frame))
	frame = append(frame, 0x15)
	return frame
}

func v5Checksum(frame []byte) byte {
	var sum byte
	for _, b := range frame[1:] {
		sum += b
	}
	return sum
}

func readV5Frame(r io.Reader) ([]byte, error) {
	header := make([]byte, 11)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}
	if header[0] != 0xA5 {
		return nil, fmt.Errorf("bad frame start 0x%02x", header[0])
	}
	plen := int(binary.LittleEndian.Uint16(header[1:3]))
	rest := make([]byte, plen+2)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, err
	}
	frame := append(header, rest...)
	if frame[len(frame)-1] != 0x15 {
		return nil, errors.New("bad frame end")
	}
	if v5Checksum(frame[:len(frame)-2]) != frame[len(frame)-2] {
		return nil, errors.New("bad frame checksum")
	}
	return frame, nil
}

func parseModbusResponse(frame []byte, slaveID byte, count uint16) ([]uint16, error) {
	// payload: frametype(1) status(1) totalworkingtime(4) powerontime(4) offsettime(4) modbus...
	const modbusOffset = 11 + 14
	if len(frame) < modbusOffset+5+2 {
		return nil, errors.New("response too short")
	}
	mb := frame[modbusOffset : len(frame)-2]
	if crc := binary.LittleEndian.Uint16(mb[len(mb)-2:]); crc != modbusCRC(mb[:len(mb)-2]) {
		return nil, errors.New("bad modbus CRC")
	}
	if mb[0] != slaveID {
		return nil, fmt.Errorf("unexpected slave id %d", mb[0])
	}
	if mb[1]&0x80 != 0 {
		return nil, fmt.Errorf("modbus exception code %d", mb[2])
	}
	n := int(mb[2])
	if n != int(count)*2 || len(mb) < 3+n+2 {
		return nil, fmt.Errorf("unexpected byte count %d", n)
	}
	regs := make([]uint16, count)
	for i := range regs {
		regs[i] = binary.BigEndian.Uint16(mb[3+i*2:])
	}
	return regs, nil
}

func modbusReadRequest(slaveID byte, start, count uint16) []byte {
	b := []byte{slaveID, 0x03}
	b = binary.BigEndian.AppendUint16(b, start)
	b = binary.BigEndian.AppendUint16(b, count)
	return binary.LittleEndian.AppendUint16(b, modbusCRC(b))
}

func modbusCRC(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}
