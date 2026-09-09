import { FormEvent, useEffect, useRef, useState } from "react";
import type { Participant, Room, Session, SignalMessage } from "./types";

const sessionKey = "voiced-session";
const apiBase = (import.meta.env.VITE_API_BASE_URL || "").replace(/\/$/, "");
const configuredWebSocketBase = (import.meta.env.VITE_WS_BASE_URL || "").replace(/\/$/, "");

type LocalAudioGraph = {
  capture: MediaStream;
  context: AudioContext;
  source: MediaStreamAudioSourceNode;
  gain: GainNode;
  destination: MediaStreamAudioDestinationNode;
};

function readSavedSession(): Session | null {
  try {
    const raw = sessionStorage.getItem(sessionKey);
    return raw ? (JSON.parse(raw) as Session) : null;
  } catch {
    sessionStorage.removeItem(sessionKey);
    return null;
  }
}

function apiURL(path: string): string {
  return `${apiBase}${path}`;
}

function webSocketURL(roomID: string): string {
  if (configuredWebSocketBase) {
    return `${configuredWebSocketBase}/ws/rooms/${encodeURIComponent(roomID)}`;
  }
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}/ws/rooms/${encodeURIComponent(roomID)}`;
}

function participantLabel(member: Participant, room: Room, me: Participant): string[] {
  const labels: string[] = [];
  if (member.id === room.ownerParticipantId) labels.push("Owner");
  if (member.id === me.id) labels.push("You");
  if (member.mutedByOwner) labels.push("Muted");
  return labels;
}

export default function App() {
  const initialSession = readSavedSession();
  const [session, setSession] = useState<Session | null>(initialSession);
  const [room, setRoom] = useState<Room | null>(initialSession?.room ?? null);
  const [participants, setParticipants] = useState<Participant[]>(initialSession ? [initialSession.participant] : []);
  const [notice, setNotice] = useState("");
  const [connectionStatus, setConnectionStatus] = useState("Disconnected");
  const [microphones, setMicrophones] = useState<MediaDeviceInfo[]>([]);
  const [selectedMicrophoneID, setSelectedMicrophoneID] = useState("");
  const [microphoneEnabled, setMicrophoneEnabled] = useState(false);
  const [inputVolume, setInputVolume] = useState(100);
  const [remoteAudioIDs, setRemoteAudioIDs] = useState<string[]>([]);
  const [remoteVolumes, setRemoteVolumes] = useState<Record<string, number>>({});
  const [remoteMuted, setRemoteMuted] = useState<Record<string, boolean>>({});

  const sessionRef = useRef<Session | null>(initialSession);
  const socketRef = useRef<WebSocket | null>(null);
  const peerConnectionRef = useRef<RTCPeerConnection | null>(null);
  const localAudioRef = useRef<LocalAudioGraph | null>(null);
  const remoteAudiosRef = useRef(new Map<string, HTMLAudioElement>());
  const audioSinkRef = useRef<HTMLDivElement | null>(null);
  const remoteCandidatesRef = useRef<RTCIceCandidateInit[]>([]);
  const localCandidatesRef = useRef<RTCIceCandidateInit[]>([]);
  const offerSentRef = useRef(false);
  const selectedMicrophoneRef = useRef("");
  const inputVolumeRef = useRef(100);
  const remoteVolumesRef = useRef<Record<string, number>>({});

  useEffect(() => {
    sessionRef.current = session;
  }, [session]);

  useEffect(() => {
    selectedMicrophoneRef.current = selectedMicrophoneID;
  }, [selectedMicrophoneID]);

  useEffect(() => {
    inputVolumeRef.current = inputVolume;
    if (localAudioRef.current) {
      localAudioRef.current.gain.gain.value = inputVolume / 100;
    }
  }, [inputVolume]);

  useEffect(() => {
    void refreshMicrophones();
    const onDeviceChange = () => { void refreshMicrophones(); };
    navigator.mediaDevices?.addEventListener("devicechange", onDeviceChange);
    return () => navigator.mediaDevices?.removeEventListener("devicechange", onDeviceChange);
  }, []);

  useEffect(() => {
    if (initialSession) {
      connectSocket(initialSession);
    }
    return () => {
      socketRef.current?.close();
      stopMicrophone();
    };
    // The saved anonymous session exists only once at page startup.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function refreshMicrophones() {
    if (!navigator.mediaDevices?.enumerateDevices) {
      setNotice("Microphone selection is not available in this browser.");
      return;
    }
    const devices = await navigator.mediaDevices.enumerateDevices();
    const inputs = devices.filter((device) => device.kind === "audioinput");
    setMicrophones(inputs);
    if (inputs.length > 0 && !inputs.some((device) => device.deviceId === selectedMicrophoneRef.current)) {
      selectedMicrophoneRef.current = inputs[0].deviceId;
      setSelectedMicrophoneID(inputs[0].deviceId);
    }
  }

  async function createOrJoin(path: string, body: Record<string, string>) {
    try {
      const response = await fetch(apiURL(path), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const result = await response.json() as Session | { error?: { message?: string } };
      if (!response.ok || !("room" in result)) {
        throw new Error((result as { error?: { message?: string } }).error?.message || "Request failed.");
      }
      beginSession(result);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "Request failed.");
    }
  }

  function beginSession(nextSession: Session) {
    socketRef.current?.close();
    sessionStorage.setItem(sessionKey, JSON.stringify(nextSession));
    sessionRef.current = nextSession;
    setSession(nextSession);
    setRoom(nextSession.room);
    setParticipants([nextSession.participant]);
    setNotice("");
    setConnectionStatus("Connecting...");
    connectSocket(nextSession);
  }

  function connectSocket(activeSession: Session) {
    const socket = new WebSocket(webSocketURL(activeSession.room.id));
    socketRef.current = socket;
    socket.onopen = () => {
      if (socketRef.current !== socket) return;
      setConnectionStatus("Connected");
      send({ type: "authenticate", payload: { participantId: activeSession.participant.id } });
    };
    socket.onmessage = (event) => {
      void handleSignal(JSON.parse(event.data) as SignalMessage).catch((error) => {
        setNotice(error instanceof Error ? error.message : "Could not process a realtime message.");
      });
    };
    socket.onerror = () => setConnectionStatus("Connection failed");
    socket.onclose = (event) => {
      if (socketRef.current !== socket) return;
      socketRef.current = null;
      finishRoom(event.reason || "Connection closed.");
    };
  }

  async function handleSignal(message: SignalMessage) {
    switch (message.type) {
      case "room_snapshot": {
        const snapshot = message.payload as { room: Room; participants: Participant[] };
        setRoom(snapshot.room);
        setParticipants(snapshot.participants);
        break;
      }
      case "participant_connected":
      case "participant_muted":
        upsertParticipant(message.payload as Participant);
        break;
      case "participant_left":
      case "participant_removed": {
        const removed = message.payload as { id: string };
        removeRemoteAudio(removed.id);
        setParticipants((current) => current.filter((member) => member.id !== removed.id));
        break;
      }
      case "owner_changed":
        setRoom((current) => current ? { ...current, ownerParticipantId: (message.payload as { ownerParticipantId: string }).ownerParticipantId } : current);
        break;
      case "answer":
        await receiveAnswer(message.payload as RTCSessionDescriptionInit);
        break;
      case "offer":
        await receiveOffer(message.payload as RTCSessionDescriptionInit);
        break;
      case "ice_candidate":
        await receiveCandidate(message.payload as RTCIceCandidateInit);
        break;
      case "error":
        setNotice((message.payload as { message?: string }).message || "The server rejected the request.");
        break;
    }
  }

  function upsertParticipant(member: Participant) {
    setParticipants((current) => {
      const index = current.findIndex((item) => item.id === member.id);
      if (index < 0) return [...current, member];
      return current.map((item) => item.id === member.id ? { ...item, ...member } : item);
    });
  }

  function send(message: SignalMessage) {
    if (socketRef.current?.readyState === WebSocket.OPEN) {
      socketRef.current.send(JSON.stringify(message));
    }
  }

  async function startMicrophone(deviceID = selectedMicrophoneRef.current) {
    if (socketRef.current?.readyState !== WebSocket.OPEN) {
      setNotice("Wait for the room connection before enabling the microphone.");
      return;
    }
    if (!navigator.mediaDevices?.getUserMedia) {
      setNotice("Microphone capture is not available in this browser.");
      return;
    }

    stopMicrophone();
    try {
      const audio: MediaTrackConstraints = {
        echoCancellation: true,
        noiseSuppression: true,
        autoGainControl: true,
      };
      if (deviceID) {
        audio.deviceId = { exact: deviceID };
      }
      const capture = await navigator.mediaDevices.getUserMedia({ audio });
      const context = new AudioContext();
      const source = context.createMediaStreamSource(capture);
      const gain = context.createGain();
      gain.gain.value = inputVolumeRef.current / 100;
      const destination = context.createMediaStreamDestination();
      source.connect(gain).connect(destination);
      await context.resume();
      localAudioRef.current = { capture, context, source, gain, destination };
      await refreshMicrophones();
      await createPeerConnection(destination.stream);
      setMicrophoneEnabled(true);
      setConnectionStatus("Connecting audio...");
    } catch (error) {
      stopMicrophone();
      setNotice(`Could not enable the microphone: ${error instanceof Error ? error.message : "Unknown error"}`);
    }
  }

  function stopMicrophone() {
    localAudioRef.current?.capture.getTracks().forEach((track) => track.stop());
    localAudioRef.current?.source.disconnect();
    localAudioRef.current?.gain.disconnect();
    void localAudioRef.current?.context.close();
    localAudioRef.current = null;
    peerConnectionRef.current?.close();
    peerConnectionRef.current = null;
    remoteCandidatesRef.current = [];
    localCandidatesRef.current = [];
    offerSentRef.current = false;
    removeAllRemoteAudio();
    setMicrophoneEnabled(false);
  }

  async function createPeerConnection(stream: MediaStream) {
    const response = await fetch(apiURL("/api/webrtc-config"));
    if (!response.ok) throw new Error("Could not load the WebRTC configuration.");
    const configuration = await response.json() as RTCConfiguration;
    const connection = new RTCPeerConnection(configuration);
    peerConnectionRef.current = connection;

    connection.onicecandidate = ({ candidate }) => {
      if (!candidate) return;
      const payload = candidate.toJSON();
      if (!offerSentRef.current) {
        localCandidatesRef.current.push(payload);
        return;
      }
      send({ type: "ice_candidate", payload });
    };
    connection.ontrack = attachRemoteAudio;
    connection.onconnectionstatechange = () => {
      if (connection.connectionState === "connected") setConnectionStatus("Audio connected");
      if (connection.connectionState === "failed") setNotice("Audio connection failed. Enable the microphone again.");
    };

    stream.getAudioTracks().forEach((track) => connection.addTrack(track, stream));
    const offer = await connection.createOffer();
    await connection.setLocalDescription(offer);
    send({ type: "offer", payload: connection.localDescription });
    offerSentRef.current = true;
    for (const candidate of localCandidatesRef.current.splice(0)) {
      send({ type: "ice_candidate", payload: candidate });
    }
  }

  async function receiveAnswer(answer: RTCSessionDescriptionInit) {
    const connection = peerConnectionRef.current;
    if (!connection || connection.signalingState !== "have-local-offer") return;
    await connection.setRemoteDescription(answer);
    await addPendingCandidates(connection);
  }

  async function receiveOffer(offer: RTCSessionDescriptionInit) {
    const connection = peerConnectionRef.current;
    if (!connection) return;
    await connection.setRemoteDescription(offer);
    await addPendingCandidates(connection);
    const answer = await connection.createAnswer();
    await connection.setLocalDescription(answer);
    send({ type: "answer", payload: connection.localDescription });
  }

  async function receiveCandidate(candidate: RTCIceCandidateInit) {
    const connection = peerConnectionRef.current;
    if (!connection || !connection.remoteDescription) {
      remoteCandidatesRef.current.push(candidate);
      return;
    }
    await connection.addIceCandidate(candidate);
  }

  async function addPendingCandidates(connection: RTCPeerConnection) {
    for (const candidate of remoteCandidatesRef.current.splice(0)) {
      await connection.addIceCandidate(candidate);
    }
  }

  function attachRemoteAudio(event: RTCTrackEvent) {
    const participantID = event.streams[0]?.id;
    if (!participantID || participantID === sessionRef.current?.participant.id) return;
    removeRemoteAudio(participantID);

    const audio = document.createElement("audio");
    audio.autoplay = true;
    audio.setAttribute("playsinline", "");
    audio.srcObject = event.streams[0] || new MediaStream([event.track]);
    audio.volume = remoteVolumesRef.current[participantID] ?? 1;
    audioSinkRef.current?.append(audio);
    remoteAudiosRef.current.set(participantID, audio);
    setRemoteAudioIDs((current) => current.includes(participantID) ? current : [...current, participantID]);
    void audio.play().catch(() => setNotice("Audio autoplay was blocked. Enable the microphone again."));
  }

  function removeRemoteAudio(participantID: string) {
    const audio = remoteAudiosRef.current.get(participantID);
    if (!audio) return;
    audio.pause();
    audio.srcObject = null;
    audio.remove();
    remoteAudiosRef.current.delete(participantID);
    setRemoteAudioIDs((current) => current.filter((id) => id !== participantID));
  }

  function removeAllRemoteAudio() {
    for (const participantID of [...remoteAudiosRef.current.keys()]) removeRemoteAudio(participantID);
  }

  function setRemoteVolume(participantID: string, value: number) {
    remoteVolumesRef.current = { ...remoteVolumesRef.current, [participantID]: value };
    const audio = remoteAudiosRef.current.get(participantID);
    if (audio) audio.volume = value;
    setRemoteVolumes((current) => ({ ...current, [participantID]: value }));
  }

  function toggleRemoteMute(participantID: string) {
    const muted = !remoteMuted[participantID];
    const audio = remoteAudiosRef.current.get(participantID);
    if (audio) audio.muted = muted;
    setRemoteMuted((current) => ({ ...current, [participantID]: muted }));
  }

  function leaveRoom() {
    send({ type: "leave", payload: null });
    finishRoom("You left the room.");
  }

  function finishRoom(message: string) {
    const socket = socketRef.current;
    socketRef.current = null;
    socket?.close();
    stopMicrophone();
    sessionRef.current = null;
    sessionStorage.removeItem(sessionKey);
    setSession(null);
    setRoom(null);
    setParticipants([]);
    setConnectionStatus("Disconnected");
    setNotice(message);
  }

  if (!session || !room) {
    return <Lobby notice={notice} onCreate={createOrJoin} onJoin={createOrJoin} />;
  }

  const me = session.participant;
  const isOwner = room.ownerParticipantId === me.id;

  return (
    <main className="app-shell">
      <section className="room-page">
        <header className="room-header">
          <div className="brand-row">
            <span className="brand-mark">V</span>
            <span>VOICED</span>
          </div>
          <div className="connection-pill"><span className={connectionStatus.includes("failed") ? "status-dot failed" : "status-dot"} />{connectionStatus}</div>
        </header>

        {notice && <div className="notice"><span>NOTICE</span><p>{notice}</p><button onClick={() => setNotice("")}>Dismiss</button></div>}

        <section className="room-hero">
          <div>
            <p className="overline">VOICE ROOM</p>
            <h1>{room.name}</h1>
            <button className="room-id" onClick={() => void navigator.clipboard.writeText(room.id)} title="Copy room ID">
              <span>ROOM ID</span>{room.id}<small>Copy</small>
            </button>
          </div>
          <div className="room-hero-actions">
            {isOwner && <button className="button danger" onClick={() => send({ type: "dissolve_room", payload: null })}>Close room</button>}
            <button className="button ghost" onClick={leaveRoom}>Leave</button>
          </div>
        </section>

        <section className="mixer-card">
          <div className="mixer-title">
            <span className={microphoneEnabled ? "mic-orb active" : "mic-orb"}>●</span>
            <div><h2>My microphone</h2><p>{microphoneEnabled ? "Sending audio" : "Off"}</p></div>
          </div>
          <label className="field-label">Input device
            <select value={selectedMicrophoneID} onChange={async (event) => {
              const nextDeviceID = event.target.value;
              selectedMicrophoneRef.current = nextDeviceID;
              setSelectedMicrophoneID(nextDeviceID);
              if (microphoneEnabled) await startMicrophone(nextDeviceID);
            }} disabled={!microphones.length}>
              {!microphones.length && <option value="">No microphone found</option>}
              {microphones.map((device, index) => <option key={device.deviceId} value={device.deviceId}>{device.label || `Microphone ${index + 1}`}</option>)}
            </select>
          </label>
          <VolumeControl label="Input volume" value={inputVolume} min={0} max={150} disabled={!microphoneEnabled} onChange={setInputVolume} />
          <div className="mixer-actions">
            <button className="text-button" onClick={() => void refreshMicrophones()}>Refresh devices</button>
            <button className={microphoneEnabled ? "button stop" : "button primary"} onClick={() => microphoneEnabled ? stopMicrophone() : void startMicrophone()}>
              {microphoneEnabled ? "Disable microphone" : "Enable microphone"}
            </button>
          </div>
        </section>

        <section className="people-section">
          <div className="section-heading"><div><p className="overline">PARTICIPANTS</p><h2>{participants.length} in room</h2></div><span className="live-chip">LIVE</span></div>
          <div className="people-grid">
            {participants.map((member) => {
              const listening = remoteAudioIDs.includes(member.id);
              const labels = participantLabel(member, room, me);
              return <article className="person-card" key={member.id}>
                <div className="person-top">
                  <div className="avatar">{member.nickname.slice(0, 1).toUpperCase()}</div>
                  <div className="person-name"><h3>{member.nickname}</h3><div className="tag-row">{labels.map((label) => <span className="tag" key={label}>{label}</span>)}</div></div>
                  <span className={member.mutedByOwner ? "voice-state muted" : listening || member.id === me.id && microphoneEnabled ? "voice-state speaking" : "voice-state"} />
                </div>
                {member.id !== me.id && listening && <div className="remote-mixer">
                  <VolumeControl label={remoteMuted[member.id] ? "Muted locally" : "Listening volume"} value={Math.round((remoteVolumes[member.id] ?? 1) * 100)} min={0} max={100} onChange={(value) => setRemoteVolume(member.id, value / 100)} />
                  <button className="icon-button" onClick={() => toggleRemoteMute(member.id)}>{remoteMuted[member.id] ? "Unmute" : "Mute locally"}</button>
                </div>}
                {member.id !== me.id && !listening && <p className="waiting-copy">Waiting for microphone</p>}
                {isOwner && member.id !== me.id && <div className="owner-actions">
                  <button className="text-button" onClick={() => send({ type: "mute_participant", payload: { targetParticipantId: member.id, muted: !member.mutedByOwner } })}>{member.mutedByOwner ? "Unmute" : "Mute"}</button>
                  <button className="text-button destructive" onClick={() => send({ type: "remove_participant", payload: { targetParticipantId: member.id } })}>Remove</button>
                </div>}
              </article>;
            })}
          </div>
        </section>
        <div ref={audioSinkRef} className="audio-sink" aria-hidden="true" />
      </section>
    </main>
  );
}

function Lobby({ notice, onCreate, onJoin }: { notice: string; onCreate: (path: string, body: Record<string, string>) => Promise<void>; onJoin: (path: string, body: Record<string, string>) => Promise<void> }) {
  const [mode, setMode] = useState<"create" | "join">("create");
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const nickname = String(form.get("nickname") || "");
    const token = String(form.get("token") || "");
    if (mode === "create") {
      void onCreate("/api/rooms", { name: String(form.get("name") || ""), nickname, token });
      return;
    }
    const roomID = String(form.get("roomId") || "");
    void onJoin(`/api/rooms/${encodeURIComponent(roomID)}/join`, { nickname, token });
  };

  return <main className="lobby-shell">
    <section className="lobby-card">
      <div className="brand-row"><span className="brand-mark">V</span><span>VOICED</span></div>
      <p className="overline">TEMPORARY VOICE ROOMS</p>
      <h1>Temporary<br />voice room.</h1>
      <p className="lobby-copy">Create a room or join with a room ID.</p>
      {notice && <div className="notice"><span>NOTICE</span><p>{notice}</p></div>}
      <div className="mode-switch"><button className={mode === "create" ? "selected" : ""} onClick={() => setMode("create")}>Create room</button><button className={mode === "join" ? "selected" : ""} onClick={() => setMode("join")}>Join room</button></div>
      <form onSubmit={submit} className="lobby-form">
        {mode === "create" ? <label className="field-label">Room name<input name="name" placeholder="Team call" maxLength={80} required /></label> : <label className="field-label">Room ID<input name="roomId" placeholder="Paste a room ID" required /></label>}
        <label className="field-label">Display name<input name="nickname" placeholder="Your name" maxLength={40} required /></label>
        <label className="field-label">Access token<input name="token" type="password" autoComplete="off" placeholder="Token required by this server" required /></label>
        <button className="button primary submit-button" type="submit">{mode === "create" ? "Create room" : "Join room"}<span>→</span></button>
      </form>
    </section>
  </main>;
}

function VolumeControl({ label, value, min, max, disabled, onChange }: { label: string; value: number; min: number; max: number; disabled?: boolean; onChange: (value: number) => void }) {
  return <label className={disabled ? "volume-control disabled" : "volume-control"}>
    <span>{label}</span><strong>{value}%</strong>
    <input type="range" min={min} max={max} value={value} disabled={disabled} onChange={(event) => onChange(Number(event.target.value))} />
  </label>;
}
