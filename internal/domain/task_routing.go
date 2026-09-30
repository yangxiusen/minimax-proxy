package domain

func (t Task) PublicStatus() V2Status {
	if t.ProtocolVersion != ProtocolTK2SD || t.Status == StatusSucceeded || t.Status == StatusFailed || t.Status == StatusCancelled {
		return t.Status.V2()
	}
	if t.RemoteCancelState == "requested" {
		return V2Running
	}
	if t.RemotePhase == RemotePreparing || t.RemotePhase == RemotePrepared || (t.RemotePhase == RemoteSubmitted && t.RemoteUpstreamStatus == "queued") {
		return V2Queued
	}
	return t.Status.V2()
}
