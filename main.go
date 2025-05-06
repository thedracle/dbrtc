package main

import (
	"bufio"
	"log"
	"net"
	"net/http"
	"os/exec"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func main() {
	// Setup media engine
	m := webrtc.MediaEngine{}
	err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeH264,
			ClockRate:    90000,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
			RTCPFeedback: nil,
		},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo)
	if err != nil {
		log.Fatal("Failed to register codec:", err)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(&m))

	http.HandleFunc("/offer", func(w http.ResponseWriter, r *http.Request) {
		log.Println("[/offer] Received SDP offer")

		// Allow CORS so browser clients can reach us
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Optional: preflight support if you add complex headers/methods
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}
		offerSDP := r.FormValue("sdp")
		if offerSDP == "" {
			http.Error(w, "missing SDP", http.StatusBadRequest)
			log.Println("[/offer] Missing SDP in POST body")
			return
		}

		log.Println("[/offer] SDP offer length:", len(offerSDP))

		peerConnection, err := api.NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			log.Println("[/offer] Error creating PeerConnection:", err)
			http.Error(w, "failed to create PeerConnection", http.StatusInternalServerError)
			return
		}

		videoTrack, err := webrtc.NewTrackLocalStaticRTP(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
			"video", "pion",
		)
		if err != nil {
			log.Println("[/offer] Error creating video track:", err)
			http.Error(w, "failed to create track", http.StatusInternalServerError)
			return
		}

		_, err = peerConnection.AddTrack(videoTrack)
		if err != nil {
			log.Println("[/offer] Error adding video track:", err)
			http.Error(w, "failed to add track", http.StatusInternalServerError)
			return
		}

		offer := webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer,
			SDP:  offerSDP,
		}

		if err := peerConnection.SetRemoteDescription(offer); err != nil {
			log.Println("[/offer] Error setting remote description:", err)
			http.Error(w, "invalid SDP", http.StatusBadRequest)
			return
		}
		log.Println("[/offer] Remote description set successfully")

		answer, err := peerConnection.CreateAnswer(nil)
		if err != nil {
			log.Println("[/offer] Error creating answer:", err)
			http.Error(w, "failed to create answer", http.StatusInternalServerError)
			return
		}

		if err := peerConnection.SetLocalDescription(answer); err != nil {
			log.Println("[/offer] Error setting local description:", err)
			http.Error(w, "failed to set local description", http.StatusInternalServerError)
			return
		}

		log.Println("[/offer] Starting ffmpeg and RTP relay")
		go startFFmpeg(videoTrack)

		<-webrtc.GatheringCompletePromise(peerConnection)

		log.Println("[/offer] ICE gathering complete, returning answer SDP")
		w.Write([]byte(peerConnection.LocalDescription().SDP))
	})

	log.Println("Server listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func startFFmpeg(videoTrack *webrtc.TrackLocalStaticRTP) {
	addr := ":5004"
	log.Println("[ffmpeg] Starting ffmpeg with RTP output to", addr)

	cmd := exec.Command("ffmpeg",
		"-re", "-stream_loop", "-1", "-i", "test.mp4",
		"-an", "-c:v", "libx264", "-f", "rtp", "rtp://127.0.0.1:5004",
	)

	stderr, _ := cmd.StderrPipe()
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Println("[ffmpeg]", scanner.Text())
		}
	}()

	if err := cmd.Start(); err != nil {
		log.Fatal("[ffmpeg] Failed to start:", err)
	}

	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		log.Fatal("[ffmpeg] Failed to listen for RTP:", err)
	}
	defer conn.Close()

	log.Println("[ffmpeg] Listening for RTP packets on", addr)

	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			log.Println("[ffmpeg] RTP read error:", err)
			break
		}

		packet := &rtp.Packet{}
		if err := packet.Unmarshal(buf[:n]); err != nil {
			log.Println("[ffmpeg] RTP unmarshal error:", err)
			continue
		}

		if err := videoTrack.WriteRTP(packet); err != nil {
			log.Println("[ffmpeg] RTP write error:", err)
			break
		}
	}
}
