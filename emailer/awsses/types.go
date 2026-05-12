package awsses

type sendEmailRequest struct {
	Content          sendEmailContent     `json:"Content"`
	Destination      sendEmailDestination `json:"Destination"`
	FromEmailAddress string               `json:"FromEmailAddress"`
}

type sendEmailContent struct {
	Simple *sendEmailMessage `json:"Simple,omitempty"`
}

type sendEmailMessage struct {
	Body    sendEmailBody         `json:"Body"`
	Subject sendEmailContentValue `json:"Subject"`
}

type sendEmailBody struct {
	HTML *sendEmailContentValue `json:"Html,omitempty"`
	Text *sendEmailContentValue `json:"Text,omitempty"`
}

type sendEmailContentValue struct {
	Charset string `json:"Charset,omitempty"`
	Data    string `json:"Data"`
}

type sendEmailDestination struct {
	ToAddresses []string `json:"ToAddresses,omitempty"`
}
