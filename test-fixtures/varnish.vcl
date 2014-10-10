import directors;    # load the directors
{{range .app}}
backend {{.Node}}_{{.ID}} { 
    .host = "{{.IP}}";
    .port = "{{.Port}}";
}{{end}}

sub vcl_init {
    new bar = directors.round_robin();
{{range .app}}
    bar.add_backend({{.Node}}_{{.ID}});{{end}}
}

sub vcl_recv {
    # send all traffic to the bar director:
    set req.backend_hint = bar.backend();
}
