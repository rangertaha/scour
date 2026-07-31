

COMMANDS:
   version  Print the version
   help, h  Shows a list of commands or help for one command

   Defining what to look for:
     add         Define an entity, or add targets, properties and aliases to one
     
     remove, rm  Remove an entity, or one of its targets, properties or rules
     templates   List the built-in schemas scour ships with

   Finding pages:
     crawl  Crawl an entity's targets, ranking discovered URLs by probability
     top    Watch every entity live, and pause or resume a crawl
     start  Let an entity be crawled again
     stop   Stop crawling an entity, keeping its frontier

   Learning where the data is:
     invalid  Label records as wrong
     rules    List the extraction rules learned for an entity
     train    Learn where an entity's properties live, from the pages already crawled

   Data:
     import  Load urls, properties, and aliases
     export  Write an entity's extracted records out as CSV, JSON or to a webhook
     list    List entities, or show everything known about one
     search  Search the records extracted for an entity

   Serving:
     mcp     Run as an MCP server over stdio
     server  Run as a service, serving the HTTP API and MCP
