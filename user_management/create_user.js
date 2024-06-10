const {google} = require('googleapis');
const identitytoolkit = google.identitytoolkit('v3');
var firebaseAdmin = require('firebase-admin');
var app = firebaseAdmin.initializeApp({
    credential: firebaseAdmin.credential.applicationDefault()
}).auth()


function generatePhoneNumber() {
    let phoneNumber = "+1";
    for(let i = 0; i < 10; i++) {
        phoneNumber += Math.floor(Math.random() * 10);
    }
    return phoneNumber;
}

const phoneNumber = generatePhoneNumber();
const email = process.argv[2];
const fullName = process.argv[3];
const password = process.argv[4];
const namespace = process.argv[5];
const role = process.argv[6];

console.log('email', email);
console.log('fullName', fullName);
console.log('password', password);
console.log('namespace', namespace);
console.log('role', role);

const generateCustomClaims = () => {
    if (role === "owner") {
        return {
            teknoir: {
                role: role,
                owner: [namespace],
                editor: [],
                viewer: []
            }
        }
    } else if (role === "editor") {
        return {
            teknoir: {
                role: role,
                owner: [],
                editor: [namespace],
                viewer: []
            }
        }
    } else if (role === "viewer") {
        return {
            teknoir: {
                role: role,
                owner: [],
                editor: [],
                viewer: [namespace]
            }
        }
    }
    console.log("Invalid role");
}


const createUser = () => {
    app
        .createUser({
            email: email,
            emailVerified: false,
            phoneNumber: phoneNumber,
            password: password,
            displayName: fullName,
            photoURL: 'http://www.example.com/12345678/photo.png',
            disabled: false,
        })
        .then((userRecord) => {
            // See the UserRecord reference doc for the contents of userRecord.
            console.log('Successfully created new user:', userRecord.uid);
            if (userRecord.toJSON()['email'] == email) {
                let user = userRecord.toJSON()
                user['customClaims'] = generateCustomClaims();
                console.log(JSON.stringify(user));

                app.setCustomUserClaims(user["uid"] , user['customClaims']).then(() => {
                    console.log("done!")
                });
            }
        })
        .catch((error) => {
            console.log('Error creating new user:', error);
        });
}
createUser();