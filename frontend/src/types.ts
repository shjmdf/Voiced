export type Participant = {
  id: string;
  nickname: string;
  joinedAt: string;
  mutedByOwner: boolean;
};

export type Room = {
  id: string;
  name: string;
  ownerParticipantId: string;
  createdAt: string;
  participantCount: number;
};

export type Session = {
  room: Room;
  participant: Participant;
};

export type SignalMessage = {
  type: string;
  payload: unknown;
};
